package main

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/livox"
)

func TestDeriveGateway(t *testing.T) {
	tests := []struct {
		ip   string
		want string
	}{
		{"192.168.1.120", "192.168.1.1"},
		{"10.42.0.233", "10.42.0.1"},
		{"172.16.5.2", "172.16.5.1"},
	}
	for _, tt := range tests {
		if got, err := deriveGateway(tt.ip); err != nil || got != tt.want {
			t.Errorf("deriveGateway(%q) = %q, %v; want %q, nil", tt.ip, got, err, tt.want)
		}
	}
	for _, bad := range []string{"not-an-ip", "", "::1", "192.168.1"} {
		if _, err := deriveGateway(bad); err == nil {
			t.Errorf("deriveGateway(%q) succeeded, want error", bad)
		}
	}
}

// fakeLidar answers one request with the given reply frames, after emitting
// any noise frames first. It reports the request it saw.
type fakeLidar struct {
	conn  net.PacketConn
	noise [][]byte
	reply func(req livox.CmdFrame) [][]byte
}

func startFakeLidar(t *testing.T, noise [][]byte, reply func(req livox.CmdFrame) [][]byte) *fakeLidar {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake lidar listen: %v", err)
	}
	f := &fakeLidar{conn: conn, noise: noise, reply: reply}
	go func() {
		buf := make([]byte, recvBufSize)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			req, err := livox.ParseCmdFrame(append([]byte(nil), buf[:n]...))
			if err != nil {
				continue
			}
			for _, frame := range f.reply(req) {
				conn.WriteTo(frame, from)
			}
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return f
}

func (f *fakeLidar) addr() *net.UDPAddr {
	return f.conn.LocalAddr().(*net.UDPAddr)
}

func udpSocket(t *testing.T) net.PacketConn {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client listen: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestReadACKHappyPath(t *testing.T) {
	lidar := startFakeLidar(t, nil, func(req livox.CmdFrame) [][]byte {
		// First send the periodic discovery broadcast (unrelated to the
		// pending command), then the real ACK.
		noise, err := livox.BuildDiscoveryQuery(999)
		if err != nil {
			t.Errorf("BuildDiscoveryQuery: %v", err)
		}
		ack, err := livox.BuildFrame(livox.CmdIDWorkModeControl, livox.CmdTypeAck,
			livox.SenderTypeLidar, req.SeqNum, []byte{0, 0, 0}, livox.ProtocolVersion)
		if err != nil {
			t.Errorf("BuildFrame: %v", err)
		}
		return [][]byte{noise, ack}
	})

	conn := udpSocket(t)
	frame, err := livox.BuildSetLidarIPFrame(livox.LidarIPConfig{
		IP: "192.168.1.120", Mask: "255.255.255.0", Gateway: "192.168.1.1",
	}, 1)
	if err != nil {
		t.Fatalf("BuildSetLidarIPFrame: %v", err)
	}
	if _, err := conn.WriteTo(frame, lidar.addr()); err != nil {
		t.Fatalf("write: %v", err)
	}

	ack, from, err := readACK(conn, make([]byte, recvBufSize), 1, 2*time.Second)
	if err != nil {
		t.Fatalf("readACK: %v", err)
	}
	if !ack.OK() {
		t.Errorf("ack = %+v, want OK", ack)
	}
	if from != "127.0.0.1" {
		t.Errorf("from = %q, want 127.0.0.1", from)
	}
}

func TestReadACKRejectingLidar(t *testing.T) {
	lidar := startFakeLidar(t, nil, func(req livox.CmdFrame) [][]byte {
		payload := []byte{1, 0x04, 0x00} // ret_code=1, error_key=0x0004
		ack, err := livox.BuildFrame(livox.CmdIDWorkModeControl, livox.CmdTypeAck,
			livox.SenderTypeLidar, req.SeqNum, payload, livox.ProtocolVersion)
		if err != nil {
			t.Errorf("BuildFrame: %v", err)
		}
		return [][]byte{ack}
	})

	conn := udpSocket(t)
	frame, err := livox.BuildSetLidarIPFrame(livox.LidarIPConfig{
		IP: "192.168.1.120", Mask: "255.255.255.0", Gateway: "192.168.1.1",
	}, 1)
	if err != nil {
		t.Fatalf("BuildSetLidarIPFrame: %v", err)
	}
	if _, err := conn.WriteTo(frame, lidar.addr()); err != nil {
		t.Fatalf("write: %v", err)
	}

	ack, _, err := readACK(conn, make([]byte, recvBufSize), 1, 2*time.Second)
	if err != nil {
		t.Fatalf("readACK: %v", err)
	}
	if ack.OK() {
		t.Errorf("ack = %+v, want not OK", ack)
	}
	if ack.ErrorKey != livox.KeyLidarIPCfg {
		t.Errorf("error_key = %#04x, want %#04x", ack.ErrorKey, livox.KeyLidarIPCfg)
	}
}

func TestReadACKTimeout(t *testing.T) {
	conn := udpSocket(t)
	if _, _, err := readACK(conn, make([]byte, recvBufSize), 1, 50*time.Millisecond); err == nil {
		t.Errorf("readACK with silent lidar succeeded, want timeout")
	}
}

func TestOpenCmdSocket(t *testing.T) {
	conn, port, err := openCmdSocket()
	if err != nil {
		t.Fatalf("openCmdSocket: %v", err)
	}
	defer conn.Close()
	if port < 1 || port > 65535 {
		t.Errorf("port = %d, want a valid UDP port", port)
	}
}

func TestRunSetIPSuccess(t *testing.T) {
	lidar := startFakeLidar(t, nil, func(req livox.CmdFrame) [][]byte {
		ack, err := livox.BuildFrame(livox.CmdIDWorkModeControl, livox.CmdTypeAck,
			livox.SenderTypeLidar, req.SeqNum, []byte{0, 0, 0}, livox.ProtocolVersion)
		if err != nil {
			t.Errorf("BuildFrame: %v", err)
		}
		return [][]byte{ack}
	})
	code := runSetIP([]string{
		"-lidar", "127.0.0.1",
		"-ip", "192.168.1.120",
		"-cmd-port", strconv.Itoa(lidar.addr().Port),
	})
	if code != 0 {
		t.Errorf("runSetIP exit = %d, want 0", code)
	}
}

func TestRunSetIPRejected(t *testing.T) {
	lidar := startFakeLidar(t, nil, func(req livox.CmdFrame) [][]byte {
		payload := []byte{1, 0x04, 0x00} // ret_code=1, error_key=0x0004
		ack, err := livox.BuildFrame(livox.CmdIDWorkModeControl, livox.CmdTypeAck,
			livox.SenderTypeLidar, req.SeqNum, payload, livox.ProtocolVersion)
		if err != nil {
			t.Errorf("BuildFrame: %v", err)
		}
		return [][]byte{ack}
	})
	code := runSetIP([]string{
		"-lidar", "127.0.0.1",
		"-ip", "192.168.1.120",
		"-cmd-port", strconv.Itoa(lidar.addr().Port),
	})
	if code != 1 {
		t.Errorf("runSetIP exit = %d, want 1", code)
	}
}

func TestRunSetIPBadUsage(t *testing.T) {
	tests := [][]string{
		{},                          // missing required flags
		{"-lidar", "192.168.1.100"}, // missing -ip
		{"-ip", "192.168.1.120"},    // missing -lidar
		{"-lidar", "192.168.1.100", "-ip", "not-an-ip"},
		{"-lidar", "192.168.1.100", "-ip", "192.168.1.120", "-mask", "300.0.0.0"},
		{"-lidar", "192.168.1.100", "-ip", "192.168.1.120", "-cmd-port", "0"},
		{"extra-arg", "-lidar", "192.168.1.100", "-ip", "192.168.1.120"},
	}
	for _, args := range tests {
		if code := runSetIP(args); code != 2 {
			t.Errorf("runSetIP(%q) exit = %d, want 2", args, code)
		}
	}
}
