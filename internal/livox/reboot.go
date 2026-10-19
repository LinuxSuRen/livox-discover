// Reboot command of the Livox SDK2 protocol: the host sends cmd 0x0200 with
// a 4-byte delay payload, the lidar ACKs with a 1-byte ret_code and then
// restarts. Rebooting is what makes a saved kKeyLidarIpCfg take effect.
package livox

import (
	"encoding/binary"
	"fmt"
)

// CmdIDRebootDevice is kCommandIDLidarRebootDevice.
const CmdIDRebootDevice uint16 = 0x0200

// RebootACKLen is the fixed size of the reboot ACK payload: u8 ret_code
// (LivoxLidarRebootResponse).
const RebootACKLen = 1

// DefaultRebootDelay is the delay value the SDK passes in its reboot
// request; its unit is defined by the firmware, so the same number is used.
const DefaultRebootDelay uint32 = 100

// BuildRebootFrame assembles the host-to-lidar reboot request. delay is the
// delay the lidar applies before restarting.
func BuildRebootFrame(delay uint32, seqNum uint32) ([]byte, error) {
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint32(payload, delay)
	return BuildFrame(CmdIDRebootDevice, CmdTypeReq, SenderTypeHost, seqNum, payload, ProtocolVersion)
}

// RebootACK is the decoded LivoxLidarRebootResponse.
type RebootACK struct {
	RetCode byte
}

// OK reports whether the lidar accepted the reboot request.
func (a RebootACK) OK() bool {
	return a.RetCode == 0
}

// ParseRebootACK validates a datagram as the ACK of a reboot request and
// decodes its 1-byte payload.
func ParseRebootACK(data []byte, seqNum uint32) (RebootACK, error) {
	frame, err := ParseCmdFrame(data)
	if err != nil {
		return RebootACK{}, err
	}
	if frame.CmdID != CmdIDRebootDevice {
		return RebootACK{}, fmt.Errorf("not a reboot frame: cmd_id=%#04x", frame.CmdID)
	}
	if frame.CmdType != CmdTypeAck {
		return RebootACK{}, fmt.Errorf("not an ack: cmd_type=%#02x", frame.CmdType)
	}
	if frame.SeqNum != seqNum {
		return RebootACK{}, fmt.Errorf("seq mismatch: got %d want %d", frame.SeqNum, seqNum)
	}
	if len(frame.Payload) != RebootACKLen {
		return RebootACK{}, fmt.Errorf("bad reboot ack len: %d != %d", len(frame.Payload), RebootACKLen)
	}
	return RebootACK{RetCode: frame.Payload[0]}, nil
}
