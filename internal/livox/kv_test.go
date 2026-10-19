package livox

import (
	"testing"
)

// Byte-exact fixture for the kKeyLidarIpCfg payload: key_num=1, reserved=0,
// key=0x0004, length=12, then ip 192.168.1.120 / mask 255.255.255.0 /
// gw 192.168.1.1. Layout cross-checked against Livox-SDK2's
// BuildSetLidarIPInfoRequest.
const fixtureSetLidarIPPayloadHex = "0100000004000c00" +
	"c0a80178" + // 192.168.1.120
	"ffffff00" + // 255.255.255.0
	"c0a80101" // 192.168.1.1

func TestBuildSetLidarIPPayloadMatchesLayout(t *testing.T) {
	got, err := BuildSetLidarIPPayload(LidarIPConfig{
		IP:      "192.168.1.120",
		Mask:    "255.255.255.0",
		Gateway: "192.168.1.1",
	})
	if err != nil {
		t.Fatalf("BuildSetLidarIPPayload: %v", err)
	}
	if want := mustHex(t, fixtureSetLidarIPPayloadHex); !equalBytes(got, want) {
		t.Errorf("payload = %x, want %x", got, want)
	}
	if len(got) != 20 {
		t.Errorf("payload len = %d, want 20", len(got))
	}
}

func TestBuildSetLidarIPPayloadRoundTrip(t *testing.T) {
	frame, err := BuildSetLidarIPFrame(LidarIPConfig{
		IP:      "192.168.1.120",
		Mask:    "255.255.255.0",
		Gateway: "192.168.1.1",
	}, 7)
	if err != nil {
		t.Fatalf("BuildSetLidarIPFrame: %v", err)
	}
	parsed, err := ParseCmdFrame(frame)
	if err != nil {
		t.Fatalf("ParseCmdFrame: %v", err)
	}
	if parsed.CmdID != CmdIDWorkModeControl {
		t.Errorf("cmd_id = %#04x, want %#04x", parsed.CmdID, CmdIDWorkModeControl)
	}
	if parsed.CmdType != CmdTypeReq {
		t.Errorf("cmd_type = %#02x, want %#02x", parsed.CmdType, CmdTypeReq)
	}
	if parsed.SenderType != SenderTypeHost {
		t.Errorf("sender_type = %#02x, want %#02x", parsed.SenderType, SenderTypeHost)
	}
	if parsed.SeqNum != 7 {
		t.Errorf("seq = %d, want 7", parsed.SeqNum)
	}
	if want := mustHex(t, fixtureSetLidarIPPayloadHex); !equalBytes(parsed.Payload, want) {
		t.Errorf("payload = %x, want %x", parsed.Payload, want)
	}
}

func TestBuildSetLidarIPPayloadRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		cfg  LidarIPConfig
	}{
		{"bad ip", LidarIPConfig{"not-an-ip", "255.255.255.0", "192.168.1.1"}},
		{"ipv6 ip", LidarIPConfig{"::1", "255.255.255.0", "192.168.1.1"}},
		{"bad mask", LidarIPConfig{"192.168.1.120", "255.255.255", "192.168.1.1"}},
		{"bad gateway", LidarIPConfig{"192.168.1.120", "255.255.255.0", "192.168.1.256"}},
		{"empty ip", LidarIPConfig{"", "255.255.255.0", "192.168.1.1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := BuildSetLidarIPPayload(tt.cfg); err == nil {
				t.Errorf("BuildSetLidarIPPayload(%+v) succeeded, want error", tt.cfg)
			}
		})
	}
}

func buildControlACK(t *testing.T, retCode byte, errorKey uint16, seqNum uint32) []byte {
	t.Helper()
	payload := []byte{retCode, byte(errorKey), byte(errorKey >> 8)}
	frame, err := BuildFrame(CmdIDWorkModeControl, CmdTypeAck, SenderTypeLidar, seqNum, payload, ProtocolVersion)
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	return frame
}

func TestParseAsyncControlACK(t *testing.T) {
	ack, _, err := ParseAsyncControlACK(buildControlACK(t, 0, 0, 42), 42)
	if err != nil {
		t.Fatalf("ParseAsyncControlACK: %v", err)
	}
	if !ack.OK() {
		t.Errorf("OK() = false for ret_code=0 error_key=0")
	}

	ack, _, err = ParseAsyncControlACK(buildControlACK(t, 1, KeyLidarIPCfg, 42), 42)
	if err != nil {
		t.Fatalf("ParseAsyncControlACK: %v", err)
	}
	if ack.OK() {
		t.Errorf("OK() = true for ret_code=1")
	}
	if ack.ErrorKey != KeyLidarIPCfg {
		t.Errorf("error_key = %#04x, want %#04x", ack.ErrorKey, KeyLidarIPCfg)
	}
}

func TestParseAsyncControlACKRejectsBadFrames(t *testing.T) {
	discoveryPayload, err := BuildDiscoveryPayload(9, "TFD2DJAAB00001", "192.0.2.10", 56100, 0)
	if err != nil {
		t.Fatalf("BuildDiscoveryPayload: %v", err)
	}
	discoveryFrame, err := BuildFrame(CmdIDDiscovery, CmdTypeAck, SenderTypeLidar, 42, discoveryPayload, ProtocolVersion)
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}

	tests := []struct {
		name   string
		frame  []byte
		seqNum uint32
	}{
		{"wrong cmd id", discoveryFrame, 42},
		{"request not ack", mustBuildFrame(t, CmdIDWorkModeControl, CmdTypeReq, SenderTypeHost, 42, []byte{0, 0, 0}), 42},
		{"seq mismatch", buildControlACK(t, 0, 0, 41), 42},
		{"short payload", mustBuildFrame(t, CmdIDWorkModeControl, CmdTypeAck, SenderTypeLidar, 42, []byte{0}), 42},
		{"truncated", buildControlACK(t, 0, 0, 42)[:20], 42},
		{"garbage", []byte{0x01, 0x02, 0x03}, 42},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := ParseAsyncControlACK(tt.frame, tt.seqNum); err == nil {
				t.Errorf("ParseAsyncControlACK(%s) succeeded, want error", tt.name)
			}
		})
	}
}

func mustBuildFrame(t *testing.T, cmdID uint16, cmdType, senderType byte, seqNum uint32, payload []byte) []byte {
	t.Helper()
	frame, err := BuildFrame(cmdID, cmdType, senderType, seqNum, payload, ProtocolVersion)
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	return frame
}
