package livox

import (
	"bytes"
	"strings"
	"testing"
)

// CRC check values from the standard CRC catalogue; the Python reference
// implementation (binascii.crc_hqx / binascii.crc32) produces the same bytes.
func TestCRC16CCITTFalse(t *testing.T) {
	tests := []struct {
		name string
		data string
		want uint16
	}{
		{"check value", "123456789", 0x29B1},
		{"empty is the init value", "", 0xFFFF},
		{"single byte", "A", 0xB915},
		{"binary", "\x00\xff\x10\x21", 0x7C93},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CRC16CCITTFalse([]byte(tt.data)); got != tt.want {
				t.Errorf("CRC16CCITTFalse(%q) = %#04x, want %#04x", tt.data, got, tt.want)
			}
		})
	}
}

func TestCRC32Payload(t *testing.T) {
	tests := []struct {
		name string
		data string
		want uint32
	}{
		{"check value", "123456789", 0xCBF43926},
		{"empty payload is zero", "", 0x0},
		{"pangram", "The quick brown fox jumps over the lazy dog", 0x414FA339},
		{"binary", "\x00\xff\x10\x21", 0x9977E9FE},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CRC32Payload([]byte(tt.data)); got != tt.want {
				t.Errorf("CRC32Payload(%q) = %#08x, want %#08x", tt.data, got, tt.want)
			}
		})
	}
}

func TestHasSOF(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty", nil, false},
		{"sof only", []byte{0xAA}, true},
		{"sof then frame", []byte{0xAA, 0x00, 0x18}, true},
		{"wrong first byte", []byte{0xAB, 0xAA}, false},
		{"sof not first", []byte{0x00, 0xAA}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasSOF(tt.data); got != tt.want {
				t.Errorf("HasSOF(%v) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

func TestBuildFrameRoundTrip(t *testing.T) {
	pattern := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(i % 251)
		}
		return b
	}
	tests := []struct {
		name    string
		payload []byte
	}{
		{"empty payload", nil},
		{"discovery payload size", pattern(24)},
		{"typical datagram", pattern(100)},
		{"max payload", pattern(MaxFrameLen - HeaderLen)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame, err := BuildFrame(CmdIDDiscovery, CmdTypeAck, SenderTypeLidar, 0xDEADBEEF, tt.payload, ProtocolVersion)
			if err != nil {
				t.Fatalf("BuildFrame: %v", err)
			}
			if len(frame) != HeaderLen+len(tt.payload) {
				t.Fatalf("frame length = %d, want %d", len(frame), HeaderLen+len(tt.payload))
			}
			parsed, err := ParseCmdFrame(frame)
			if err != nil {
				t.Fatalf("ParseCmdFrame: %v", err)
			}
			if parsed.SOF != SOF || parsed.Version != ProtocolVersion {
				t.Errorf("header = {%#02x, %#02x}, want {%#02x, %#02x}", parsed.SOF, parsed.Version, SOF, ProtocolVersion)
			}
			if parsed.Length != uint16(len(frame)) {
				t.Errorf("Length = %d, want %d", parsed.Length, len(frame))
			}
			if parsed.SeqNum != 0xDEADBEEF {
				t.Errorf("SeqNum = %#x, want 0xdeadbeef", parsed.SeqNum)
			}
			if parsed.CmdID != CmdIDDiscovery || parsed.CmdType != CmdTypeAck || parsed.SenderType != SenderTypeLidar {
				t.Errorf("cmd fields = {%#04x, %#02x, %#02x}, want {%#04x, %#02x, %#02x}",
					parsed.CmdID, parsed.CmdType, parsed.SenderType, CmdIDDiscovery, CmdTypeAck, SenderTypeLidar)
			}
			if !bytes.Equal(parsed.Payload, tt.payload) {
				t.Errorf("payload mismatch: got %d bytes, want %d bytes", len(parsed.Payload), len(tt.payload))
			}
		})
	}
}

func TestBuildFrameTooLarge(t *testing.T) {
	_, err := BuildFrame(CmdIDDiscovery, CmdTypeReq, SenderTypeHost, 1, make([]byte, MaxFrameLen-HeaderLen+1), ProtocolVersion)
	if err == nil || !strings.Contains(err.Error(), "exceeds max") {
		t.Errorf("error = %v, want one mentioning the max frame length", err)
	}
}

func TestParseCmdFrameErrors(t *testing.T) {
	valid, err := BuildDiscoveryQuery(7)
	if err != nil {
		t.Fatalf("BuildDiscoveryQuery: %v", err)
	}

	withLength := func(n uint16) []byte {
		clone := append([]byte(nil), valid...)
		clone[2] = byte(n)
		clone[3] = byte(n >> 8)
		return clone
	}

	tests := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{"nil datagram", nil, "truncated"},
		{"shorter than header", valid[:HeaderLen-1], "truncated"},
		{"wrong sof", append([]byte{0x55}, valid[1:]...), "bad sof"},
		{"length below header", withLength(HeaderLen - 1), "bad length"},
		{"length above max", withLength(MaxFrameLen + 1), "bad length"},
		{"length beyond datagram", withLength(HeaderLen + 1), "bad length"},
		{"header corruption fails crc16", flipByte(valid, 5), "crc16 mismatch"},
		{"extra trailing bytes are ignored", append(append([]byte(nil), valid...), 0xFF, 0xFF), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCmdFrame(tt.data)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("ParseCmdFrame error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ParseCmdFrame error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseCmdFramePayloadCRC(t *testing.T) {
	payload, err := BuildDiscoveryPayload(9, "TFD2DJAAB00001", "192.0.2.10", 56100, 0)
	if err != nil {
		t.Fatalf("BuildDiscoveryPayload: %v", err)
	}
	frame, err := BuildFrame(CmdIDDiscovery, CmdTypeAck, SenderTypeLidar, 42, payload, ProtocolVersion)
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	if _, err := ParseCmdFrame(flipByte(frame, 30)); err == nil || !strings.Contains(err.Error(), "crc32 mismatch") {
		t.Errorf("error = %v, want crc32 mismatch", err)
	}
	if _, err := ParseCmdFrame(frame); err != nil {
		t.Errorf("ParseCmdFrame(valid) error = %v, want nil", err)
	}
}

// flipByte returns a copy of data with one byte incremented, so the copy is
// guaranteed to differ from the original even when the byte wraps.
func flipByte(data []byte, off int) []byte {
	clone := append([]byte(nil), data...)
	clone[off]++
	return clone
}
