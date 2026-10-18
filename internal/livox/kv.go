// Key-value control commands of the Livox SDK2 protocol. Besides plain
// fixed-layout commands like discovery, SDK2 carries most configuration as
// key/value payloads under cmd 0x0100 (kCommandIDLidarWorkModeControl); the
// lidar answers with a 3-byte {ret_code, error_key} ACK. This file implements
// the kKeyLidarIpCfg (0x0004) write used to change a lidar's own IP.
package livox

import (
	"encoding/binary"
	"fmt"
	"net"
)

// Key-value command identifiers and payload layout constants, from the
// Livox-SDK2 sources (comm/define.h, include/livox_lidar_def.h).
const (
	// CmdIDWorkModeControl is the command id every key-value control request
	// and its ACK use (kCommandIDLidarWorkModeControl).
	CmdIDWorkModeControl uint16 = 0x0100
	// KeyLidarIPCfg is the parameter key that carries the lidar's own
	// ip / mask / gateway triple (kKeyLidarIpCfg).
	KeyLidarIPCfg uint16 = 0x0004
	// LidarIPCfgValueLen is the fixed size of the key 0x0004 value: three
	// dotted quads.
	LidarIPCfgValueLen = 12
	// AsyncControlACKLen is the fixed size of a key-value control ACK
	// payload: u8 ret_code + u16 error_key (LivoxLidarAsyncControlResponse).
	AsyncControlACKLen = 3
)

// LidarIPConfig is the new network configuration written to a lidar via
// kKeyLidarIpCfg. All three fields are dotted-quad IPv4 strings.
type LidarIPConfig struct {
	IP      string
	Mask    string
	Gateway string
}

// BuildSetLidarIPPayload packs the 20-byte key-value request payload that
// carries one kKeyLidarIpCfg entry:
//
//	u16 key_num = 1 | u16 reserved = 0 | u16 key = 0x0004 |
//	u16 length = 12 | ip[4] mask[4] gw[4]
//
// It mirrors Livox-SDK2's BuildSetLidarIPInfoRequest byte for byte.
func BuildSetLidarIPPayload(cfg LidarIPConfig) ([]byte, error) {
	ip, err := parseIPv4(cfg.IP)
	if err != nil {
		return nil, fmt.Errorf("invalid lidar ip: %w", err)
	}
	mask, err := parseIPv4(cfg.Mask)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet mask: %w", err)
	}
	gw, err := parseIPv4(cfg.Gateway)
	if err != nil {
		return nil, fmt.Errorf("invalid gateway: %w", err)
	}

	buf := make([]byte, 0, 20)
	buf = binary.LittleEndian.AppendUint16(buf, 1) // key_num
	buf = binary.LittleEndian.AppendUint16(buf, 0) // reserved
	buf = binary.LittleEndian.AppendUint16(buf, KeyLidarIPCfg)
	buf = binary.LittleEndian.AppendUint16(buf, LidarIPCfgValueLen)
	buf = append(buf, ip[:]...)
	buf = append(buf, mask[:]...)
	buf = append(buf, gw[:]...)
	return buf, nil
}

// BuildSetLidarIPFrame wraps the kKeyLidarIpCfg payload in a complete
// host-to-lidar command frame, ready to be unicast to the lidar's command
// port (56100 for the Mid360 family).
func BuildSetLidarIPFrame(cfg LidarIPConfig, seqNum uint32) ([]byte, error) {
	payload, err := BuildSetLidarIPPayload(cfg)
	if err != nil {
		return nil, err
	}
	return BuildFrame(CmdIDWorkModeControl, CmdTypeReq, SenderTypeHost, seqNum, payload, ProtocolVersion)
}

// AsyncControlACK is the decoded LivoxLidarAsyncControlResponse: ret_code 0
// with error_key 0 means the lidar accepted the request.
type AsyncControlACK struct {
	RetCode  byte
	ErrorKey uint16
}

// OK reports whether the lidar accepted the control request.
func (a AsyncControlACK) OK() bool {
	return a.RetCode == 0 && a.ErrorKey == 0
}

// ParseAsyncControlACK validates a datagram as the ACK of a key-value
// control request and decodes its 3-byte payload.
func ParseAsyncControlACK(data []byte, seqNum uint32) (AsyncControlACK, CmdFrame, error) {
	frame, err := ParseCmdFrame(data)
	if err != nil {
		return AsyncControlACK{}, frame, err
	}
	if frame.CmdID != CmdIDWorkModeControl {
		return AsyncControlACK{}, frame, fmt.Errorf("not a control frame: cmd_id=%#04x", frame.CmdID)
	}
	if frame.CmdType != CmdTypeAck {
		return AsyncControlACK{}, frame, fmt.Errorf("not an ack: cmd_type=%#02x", frame.CmdType)
	}
	if frame.SeqNum != seqNum {
		return AsyncControlACK{}, frame, fmt.Errorf("seq mismatch: got %d want %d", frame.SeqNum, seqNum)
	}
	if len(frame.Payload) != AsyncControlACKLen {
		return AsyncControlACK{}, frame, fmt.Errorf("bad control ack len: %d != %d", len(frame.Payload), AsyncControlACKLen)
	}
	return AsyncControlACK{
		RetCode:  frame.Payload[0],
		ErrorKey: binary.LittleEndian.Uint16(frame.Payload[1:]),
	}, frame, nil
}

// parseIPv4 decodes one dotted-quad address into 4 bytes, rejecting IPv6 and
// shorthand forms.
func parseIPv4(s string) ([4]byte, error) {
	var out [4]byte
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil {
		return out, fmt.Errorf("%q is not an IPv4 address", s)
	}
	copy(out[:], ip.To4())
	return out, nil
}
