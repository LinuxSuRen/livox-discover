// Package livox implements the parts of the Livox SDK2 command protocol the
// discovery tool needs: the 24-byte frame header protected by a CRC-16 and a
// CRC-32, plus the fixed 24-byte discovery ACK payload.
package livox

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

// Protocol constants of the Livox SDK2 command frame.
const (
	// SOF is the start-of-frame marker every SDK2 command frame begins with.
	SOF byte = 0xAA
	// ProtocolVersion is the SDK2 protocol version field written by hosts.
	ProtocolVersion byte = 0x00
	// HeaderLen is the size of the frame header (sof through crc32),
	// payload excluded.
	HeaderLen = 24
	// MaxFrameLen is the maximum total frame size the SDK accepts.
	MaxFrameLen = 1400
	// DetectionPayloadLen is the fixed size of a discovery ACK payload.
	DetectionPayloadLen = 24
)

// Command identifiers and types.
const (
	CmdIDDiscovery uint16 = 0x0000
	CmdTypeReq     byte   = 0x00
	CmdTypeAck     byte   = 0x01
	SenderTypeHost byte   = 0x00
	// SenderTypeLidar marks frames sent by a device.
	SenderTypeLidar byte = 0x01
)

// Byte offsets inside the frame header. All multi-byte fields are
// little-endian.
const (
	sofOffset        = 0  // u8  start of frame (0xAA)
	versionOffset    = 1  // u8  protocol version
	lengthOffset     = 2  // u16 total frame length, header included
	seqNumOffset     = 4  // u32 sequence number
	cmdIDOffset      = 8  // u16 command id
	cmdTypeOffset    = 10 // u8  request / ack
	senderTypeOffset = 11 // u8  host / lidar
	crc16Offset      = 18 // u16 CRC-16/CCITT-FALSE over bytes [0, 18)
	crc32Offset      = 20 // u32 CRC-32 (IEEE) over the payload
	payloadOffset    = 24
)

// CRC16CCITTFalse computes CRC-16/CCITT-FALSE (poly 0x1021, init 0xFFFF, no
// reflection, no final XOR). It matches Python's binascii.crc_hqx(data, 0xFFFF).
func CRC16CCITTFalse(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// CRC32Payload returns the IEEE CRC-32 of data, which is what the SDK2 frame
// carries in its trailing header field. An empty payload yields 0.
func CRC32Payload(data []byte) uint32 {
	return crc32.ChecksumIEEE(data)
}

// HasSOF reports whether a datagram begins with the SDK2 start-of-frame byte.
func HasSOF(data []byte) bool {
	return len(data) > 0 && data[0] == SOF
}

// CmdFrame is a parsed SDK2 command frame. Payload is always a copy, so it
// stays valid even when the receive buffer is reused.
type CmdFrame struct {
	SOF        byte
	Version    byte
	Length     uint16
	SeqNum     uint32
	CmdID      uint16
	CmdType    byte
	SenderType byte
	Payload    []byte
}

// BuildFrame assembles a complete frame, header CRCs included. version is
// normally ProtocolVersion.
func BuildFrame(cmdID uint16, cmdType, senderType byte, seqNum uint32, payload []byte, version byte) ([]byte, error) {
	total := HeaderLen + len(payload)
	if total > MaxFrameLen {
		return nil, fmt.Errorf("frame length %d exceeds max %d", total, MaxFrameLen)
	}
	buf := make([]byte, total)
	buf[sofOffset] = SOF
	buf[versionOffset] = version
	binary.LittleEndian.PutUint16(buf[lengthOffset:], uint16(total))
	binary.LittleEndian.PutUint32(buf[seqNumOffset:], seqNum)
	binary.LittleEndian.PutUint16(buf[cmdIDOffset:], cmdID)
	buf[cmdTypeOffset] = cmdType
	buf[senderTypeOffset] = senderType
	binary.LittleEndian.PutUint16(buf[crc16Offset:], CRC16CCITTFalse(buf[:crc16Offset]))
	binary.LittleEndian.PutUint32(buf[crc32Offset:], CRC32Payload(payload))
	copy(buf[payloadOffset:], payload)
	return buf, nil
}

// BuildDiscoveryQuery returns the frame a host broadcasts to ask nearby
// lidars for a discovery ACK.
func BuildDiscoveryQuery(seqNum uint32) ([]byte, error) {
	return BuildFrame(CmdIDDiscovery, CmdTypeReq, SenderTypeHost, seqNum, nil, ProtocolVersion)
}

// ParseCmdFrame validates a datagram against the frame format and decodes it.
// Trailing bytes beyond the declared length are ignored.
func ParseCmdFrame(data []byte) (CmdFrame, error) {
	if len(data) < HeaderLen {
		return CmdFrame{}, fmt.Errorf("truncated: %d bytes < header %d", len(data), HeaderLen)
	}
	if data[sofOffset] != SOF {
		return CmdFrame{}, fmt.Errorf("bad sof: %#02x != %#02x", data[sofOffset], SOF)
	}
	length := binary.LittleEndian.Uint16(data[lengthOffset:])
	if length < HeaderLen {
		return CmdFrame{}, fmt.Errorf("bad length: %d < header %d", length, HeaderLen)
	}
	if length > MaxFrameLen {
		return CmdFrame{}, fmt.Errorf("bad length: %d > max %d", length, MaxFrameLen)
	}
	if int(length) > len(data) {
		return CmdFrame{}, fmt.Errorf("bad length: %d > datagram %d", length, len(data))
	}

	storedCRC16 := binary.LittleEndian.Uint16(data[crc16Offset:])
	calcCRC16 := CRC16CCITTFalse(data[:crc16Offset])
	if storedCRC16 != calcCRC16 {
		return CmdFrame{}, fmt.Errorf("crc16 mismatch: stored=%#04x calc=%#04x", storedCRC16, calcCRC16)
	}

	payload := append([]byte(nil), data[payloadOffset:length]...)
	storedCRC32 := binary.LittleEndian.Uint32(data[crc32Offset:])
	if storedCRC32 != CRC32Payload(payload) {
		return CmdFrame{}, fmt.Errorf("crc32 mismatch: stored=%#08x calc=%#08x", storedCRC32, CRC32Payload(payload))
	}

	return CmdFrame{
		SOF:        data[sofOffset],
		Version:    data[versionOffset],
		Length:     length,
		SeqNum:     binary.LittleEndian.Uint32(data[seqNumOffset:]),
		CmdID:      binary.LittleEndian.Uint16(data[cmdIDOffset:]),
		CmdType:    data[cmdTypeOffset],
		SenderType: data[senderTypeOffset],
		Payload:    payload,
	}, nil
}
