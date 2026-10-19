package livox

import "testing"

func TestBuildRebootFrameLayout(t *testing.T) {
	frame, err := BuildRebootFrame(100, 9)
	if err != nil {
		t.Fatalf("BuildRebootFrame: %v", err)
	}
	parsed, err := ParseCmdFrame(frame)
	if err != nil {
		t.Fatalf("ParseCmdFrame: %v", err)
	}
	if parsed.CmdID != CmdIDRebootDevice {
		t.Errorf("cmd_id = %#04x, want %#04x", parsed.CmdID, CmdIDRebootDevice)
	}
	if parsed.SeqNum != 9 {
		t.Errorf("seq = %d, want 9", parsed.SeqNum)
	}
	if len(parsed.Payload) != 4 || binaryLE(parsed.Payload) != 100 {
		t.Errorf("payload = %x, want 100 little-endian", parsed.Payload)
	}
}

func binaryLE(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func TestParseRebootACK(t *testing.T) {
	ack, err := mustBuildRebootACK(t, 0, 5)
	if err != nil {
		t.Fatalf("ParseRebootACK: %v", err)
	}
	if !ack.OK() {
		t.Errorf("OK() = false for ret_code=0")
	}

	ack, err = mustBuildRebootACK(t, 1, 5)
	if err != nil {
		t.Fatalf("ParseRebootACK: %v", err)
	}
	if ack.OK() {
		t.Errorf("OK() = true for ret_code=1")
	}
}

func mustBuildRebootACK(t *testing.T, retCode byte, seq uint32) (RebootACK, error) {
	t.Helper()
	frame := mustBuildFrame(t, CmdIDRebootDevice, CmdTypeAck, SenderTypeLidar, seq, []byte{retCode})
	return ParseRebootACK(frame, seq)
}

func TestParseRebootACKRejectsBadFrames(t *testing.T) {
	rebootACK := mustBuildFrame(t, CmdIDRebootDevice, CmdTypeAck, SenderTypeLidar, 7, []byte{0})
	tests := []struct {
		name   string
		frame  []byte
		seqNum uint32
	}{
		{"wrong cmd id", mustBuildFrame(t, CmdIDDiscovery, CmdTypeAck, SenderTypeLidar, 7, []byte{0}), 7},
		{"request not ack", mustBuildFrame(t, CmdIDRebootDevice, CmdTypeReq, SenderTypeHost, 7, []byte{0}), 7},
		{"seq mismatch", rebootACK, 8},
		{"payload too long", mustBuildFrame(t, CmdIDRebootDevice, CmdTypeAck, SenderTypeLidar, 7, []byte{0, 0}), 7},
		{"truncated", rebootACK[:10], 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseRebootACK(tt.frame, tt.seqNum); err == nil {
				t.Errorf("ParseRebootACK(%s) succeeded, want error", tt.name)
			}
		})
	}
}
