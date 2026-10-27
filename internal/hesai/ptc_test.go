package hesai

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

func TestBuildFrame(t *testing.T) {
	frame := BuildFrame(CmdSetNet, []byte{1, 2, 3})
	want := []byte{0x47, 0x74, CmdSetNet, 0x00, 0x00, 0x00, 0x00, 0x03, 1, 2, 3}
	if !bytes.Equal(frame, want) {
		t.Errorf("BuildFrame = % x, want % x", frame, want)
	}
	if frame := BuildFrame(CmdRebootLidar, nil); len(frame) != headerSize {
		t.Errorf("empty payload frame length = %d, want %d", len(frame), headerSize)
	}
}

func TestBuildSetNetPayload(t *testing.T) {
	payload, err := BuildSetNetPayload(NetConfig{
		IP: "192.168.124.20", Mask: "255.255.255.0", Gateway: "192.168.124.1",
		VLANID: 100,
	})
	if err != nil {
		t.Fatalf("BuildSetNetPayload: %v", err)
	}
	if len(payload) != 15 {
		t.Fatalf("payload length = %d, want 15", len(payload))
	}
	want := []byte{
		192, 168, 124, 20, // IP
		255, 255, 255, 0, // mask
		192, 168, 124, 1, // gateway
		0x00,       // VLAN disabled
		0x00, 0x64, // VLAN id 100, big-endian
	}
	if !bytes.Equal(payload, want) {
		t.Errorf("payload = % x, want % x", payload, want)
	}

	for name, cfg := range map[string]NetConfig{
		"bad ip":      {IP: "nope", Mask: "255.255.255.0", Gateway: "192.168.1.1"},
		"bad mask":    {IP: "192.168.1.2", Mask: "24bits", Gateway: "192.168.1.1"},
		"bad gateway": {IP: "192.168.1.2", Mask: "255.255.255.0", Gateway: "::1"},
		"ipv6 is out": {IP: "::1", Mask: "255.255.255.0", Gateway: "192.168.1.1"},
		"empty ip":    {IP: "", Mask: "255.255.255.0", Gateway: "192.168.1.1"},
		"empty gw":    {IP: "192.168.1.2", Mask: "255.255.255.0", Gateway: ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildSetNetPayload(cfg); err == nil {
				t.Errorf("BuildSetNetPayload(%+v) succeeded, want an error", cfg)
			}
		})
	}
}

// fakeLidar answers PTC requests over TCP the way a Hesai lidar does:
// every command in answers maps to the payload it returns with return code
// zero; prefix, when set, is written ahead of the first response frame so
// the client's header resynchronization gets exercised.
type fakeLidar struct {
	answers map[byte][]byte
	prefix  []byte
}

func startFakeLidar(t *testing.T, f fakeLidar) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	go serveFakeLidar(l, f)
	return l.Addr().String()
}

func serveFakeLidar(l net.Listener, f fakeLidar) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer conn.Close()
			wrotePrefix := false
			header := make([]byte, headerSize)
			for {
				if _, err := io.ReadFull(conn, header); err != nil {
					return
				}
				if length := binary.BigEndian.Uint32(header[4:8]); length > 0 {
					if _, err := io.CopyN(io.Discard, conn, int64(length)); err != nil {
						return
					}
				}
				if !wrotePrefix && len(f.prefix) > 0 {
					conn.Write(f.prefix)
					wrotePrefix = true
				}
				frame := BuildFrame(header[2], f.answers[header[2]])
				if header[2] == 0xEE { // magic command: answer with return code 4
					frame[3] = 4
				}
				conn.Write(frame)
			}
		}(conn)
	}
}

func TestClientExchange(t *testing.T) {
	t.Run("set-net accepted", func(t *testing.T) {
		addr := startFakeLidar(t, fakeLidar{answers: map[byte][]byte{CmdSetNet: nil}})
		client, err := Dial(addr, 2*time.Second)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer client.Close()
		err = client.SetNet(NetConfig{IP: "192.168.124.20", Mask: "255.255.255.0", Gateway: "192.168.124.1"})
		if err != nil {
			t.Fatalf("SetNet: %v", err)
		}
	})

	t.Run("calibration payload round-trips", func(t *testing.T) {
		addr := startFakeLidar(t, fakeLidar{answers: map[byte][]byte{
			CmdGetLidarCalibration: bytes.Repeat([]byte{0xAB}, 4096),
		}})
		client, err := Dial(addr, 2*time.Second)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer client.Close()
		payload, err := client.GetLidarCalibration()
		if err != nil {
			t.Fatalf("GetLidarCalibration: %v", err)
		}
		if len(payload) != 4096 {
			t.Errorf("payload length = %d, want 4096", len(payload))
		}
	})

	t.Run("stray bytes before the response are skipped", func(t *testing.T) {
		addr := startFakeLidar(t, fakeLidar{
			answers: map[byte][]byte{CmdRebootLidar: nil},
			prefix:  []byte{0x00, 0x47, 0x00, 0x74},
		})
		client, err := Dial(addr, 2*time.Second)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer client.Close()
		if err := client.RebootLidar(); err != nil {
			t.Fatalf("RebootLidar: %v", err)
		}
	})

	t.Run("rejection is a CommandError", func(t *testing.T) {
		// 0xEE as a command byte makes the fake answer with return code 4.
		addr := startFakeLidar(t, fakeLidar{answers: map[byte][]byte{0xEE: nil}})
		client, err := Dial(addr, 2*time.Second)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer client.Close()
		_, err = client.Exchange(0xEE, nil)
		var cmdErr *CommandError
		if !errors.As(err, &cmdErr) {
			t.Fatalf("Exchange error = %v, want a *CommandError", err)
		}
		if cmdErr.RetCode != 4 {
			t.Errorf("CommandError.RetCode = %d, want 4", cmdErr.RetCode)
		}
	})

	t.Run("dead port fails to dial", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := l.Addr().String()
		l.Close()
		if _, err := Dial(addr, 500*time.Millisecond); err == nil {
			t.Fatal("Dial to a closed port succeeded")
		}
	})
}

func TestReadHeaderResync(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{"clean header", []byte{0x47, 0x74, 0x21, 0x00, 0, 0, 0, 0}},
		{"leading noise", []byte{0xAA, 0xBB, 0x47, 0x00, 0x47, 0x74, 0x21, 0x00, 0, 0, 0, 0}},
		{"double marker start", []byte{0x47, 0x47, 0x74, 0x10, 0x00, 0, 0, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := make([]byte, headerSize)
			if err := readHeader(&oneByteReader{data: tt.in}, header); err != nil {
				t.Fatalf("readHeader error = %v", err)
			}
			want := tt.in[len(tt.in)-headerSize:]
			if !bytes.Equal(header, want) {
				t.Errorf("header = % x, want the trailing % x", header, want)
			}
		})
	}

	t.Run("stream ends before a marker", func(t *testing.T) {
		r := &oneByteReader{data: []byte{0x47, 0x00, 0x47}}
		if err := readHeader(r, make([]byte, headerSize)); err == nil {
			t.Fatal("readHeader succeeded on a truncated stream")
		}
	})
}

// oneByteReader hands out its data one byte per Read, the worst case for a
// resynchronizing parser.
type oneByteReader struct {
	data []byte
	pos  int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, fmt.Errorf("EOF")
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}
