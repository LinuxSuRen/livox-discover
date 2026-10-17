package livox

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// Byte offsets inside the 24-byte discovery ACK payload
// (1 + 1 + 16 + 4 + 2 = 24).
const (
	detRetCodeOff = 0 // u8  return code, 0 on success
	detDevTypeOff = 1 // u8  device type, see devTypeNames
	detSNOff      = 2 // char[16], NUL padded
	detSNLen      = 16
	detIPOff      = 18 // u8[4] dotted quad, network byte order
	detIPLen      = 4
	detCmdPortOff = 22 // u16 little-endian
)

// devTypeNames maps the dev_type byte of a discovery ACK to the product name
// it identifies (values from livox_lidar_def.h).
var devTypeNames = map[byte]string{
	9:  "Mid360",
	35: "Mid360S",
	40: "Avia2",
	41: "Mid360L",
}

// DevTypeName returns the product name for a dev_type byte, or an
// "unknown(N)" placeholder for unmapped values.
func DevTypeName(devType byte) string {
	if name, ok := devTypeNames[devType]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", devType)
}

// DetectionData is the decoded discovery ACK payload.
type DetectionData struct {
	RetCode byte
	DevType byte
	SN      string
	LidarIP string
	CmdPort uint16
}

// OK reports whether the device answered with a success return code. A
// non-zero RetCode still parses; callers decide how to report it.
func (d DetectionData) OK() bool {
	return d.RetCode == 0
}

// DevTypeName returns the product name of the answering device.
func (d DetectionData) DevTypeName() string {
	return DevTypeName(d.DevType)
}

// MarshalJSON renders the numeric fields plus the human-readable
// dev_type_name, so JSON consumers do not have to map dev_type themselves.
func (d DetectionData) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		RetCode     byte   `json:"ret_code"`
		DevType     byte   `json:"dev_type"`
		DevTypeName string `json:"dev_type_name"`
		SN          string `json:"sn"`
		LidarIP     string `json:"lidar_ip"`
		CmdPort     uint16 `json:"cmd_port"`
	}{
		RetCode:     d.RetCode,
		DevType:     d.DevType,
		DevTypeName: d.DevTypeName(),
		SN:          d.SN,
		LidarIP:     d.LidarIP,
		CmdPort:     d.CmdPort,
	})
}

// BuildDiscoveryPayload packs a 24-byte discovery ACK payload; it doubles as
// the fixture builder for tests.
func BuildDiscoveryPayload(devType byte, sn, lidarIP string, cmdPort uint16, retCode byte) ([]byte, error) {
	snBytes := []byte(sn)
	if len(snBytes) > detSNLen {
		return nil, fmt.Errorf("sn too long: %d > %d", len(snBytes), detSNLen)
	}
	ip := net.ParseIP(lidarIP)
	if ip == nil || ip.To4() == nil {
		return nil, fmt.Errorf("invalid IPv4 address %q", lidarIP)
	}
	buf := make([]byte, DetectionPayloadLen)
	buf[detRetCodeOff] = retCode
	buf[detDevTypeOff] = devType
	copy(buf[detSNOff:], snBytes)
	copy(buf[detIPOff:], ip.To4())
	binary.LittleEndian.PutUint16(buf[detCmdPortOff:], cmdPort)
	return buf, nil
}

// ParseDiscoveryPayload decodes a 24-byte discovery ACK payload. A non-zero
// ret_code is not an error here; it is surfaced in the result.
func ParseDiscoveryPayload(payload []byte) (DetectionData, error) {
	if len(payload) != DetectionPayloadLen {
		return DetectionData{}, fmt.Errorf("bad detection payload len: %d != %d", len(payload), DetectionPayloadLen)
	}
	return DetectionData{
		RetCode: payload[detRetCodeOff],
		DevType: payload[detDevTypeOff],
		SN:      snFromBytes(payload[detSNOff : detSNOff+detSNLen]),
		LidarIP: net.IP(append([]byte(nil), payload[detIPOff:detIPOff+detIPLen]...)).String(),
		CmdPort: binary.LittleEndian.Uint16(payload[detCmdPortOff:]),
	}, nil
}

// ParseDiscoveryACK validates a whole datagram as a command frame and decodes
// its discovery payload. Any violation of the frame format is an error.
func ParseDiscoveryACK(data []byte) (DetectionData, error) {
	frame, err := ParseCmdFrame(data)
	if err != nil {
		return DetectionData{}, err
	}
	if frame.CmdID != CmdIDDiscovery {
		return DetectionData{}, fmt.Errorf("not a discovery frame: cmd_id=%#04x", frame.CmdID)
	}
	return ParseDiscoveryPayload(frame.Payload)
}

// snFromBytes decodes the fixed 16-byte SN field: trailing NUL padding is
// trimmed and bytes that are not printable ASCII are replaced with '?'.
func snFromBytes(b []byte) string {
	end := len(b)
	for end > 0 && b[end-1] == 0 {
		end--
	}
	var sb strings.Builder
	for _, c := range b[:end] {
		if c >= 0x20 && c < 0x7f {
			sb.WriteByte(c)
		} else {
			sb.WriteByte('?')
		}
	}
	return sb.String()
}
