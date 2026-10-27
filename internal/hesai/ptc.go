package hesai

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// PTC ("programmable test & control") is the request/response protocol
// Hesai lidars serve over TCP. This file implements protocol version 1.0,
// which is what a JT128 answers with when spoken to plainly.

const (
	// DefaultPTCPort is the TCP port Hesai lidars serve PTC on.
	DefaultPTCPort = 9347

	// PTC commands this tool uses (command codes from the Hesai SDK,
	// PtcClient/include/ptc_client.h).
	CmdGetLidarCalibration byte = 0x05 // read-only; used as a channel probe
	CmdRebootLidar         byte = 0x10
	CmdSetNet              byte = 0x21

	// Frame markers and layout of PTC 1.0.
	headerIdent0 = 0x47
	headerIdent1 = 0x74
	headerSize   = 8 // ident0, ident1, cmd, return_code, payload_len (u32 BE)

	// maxACKPayload bounds the payload length a response header may announce,
	// so a corrupt length cannot make the client buffer without limit. The
	// largest payload this tool pulls is a calibration file (tens of KiB).
	maxACKPayload = 4 << 20
)

// BuildFrame encodes one PTC 1.0 request: the 0x47 0x74 marker, the command,
// a zero return code and the payload length in big-endian order, followed by
// the payload itself. PTC 1.0 carries no checksums.
func BuildFrame(cmd byte, payload []byte) []byte {
	frame := make([]byte, headerSize+len(payload))
	frame[0] = headerIdent0
	frame[1] = headerIdent1
	frame[2] = cmd
	frame[3] = 0 // return code, always 0 in a request
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[headerSize:], payload)
	return frame
}

// NetConfig is a lidar network configuration as SetNet stores it.
type NetConfig struct {
	IP          string
	Mask        string
	Gateway     string
	VLANEnabled bool
	VLANID      uint16
}

// BuildSetNetPayload packs the 15-byte SetNet value: IP, mask and gateway as
// dotted quads, then a VLAN flag byte and the VLAN id as a big-endian u16.
// The lidar rejects an inconsistent triple with a non-zero return code.
func BuildSetNetPayload(cfg NetConfig) ([]byte, error) {
	ip := net.ParseIP(cfg.IP)
	if ip == nil || ip.To4() == nil {
		return nil, fmt.Errorf("%q is not an IPv4 address", cfg.IP)
	}
	mask := net.ParseIP(cfg.Mask)
	if mask == nil || mask.To4() == nil {
		return nil, fmt.Errorf("mask %q is not an IPv4 address", cfg.Mask)
	}
	gateway := net.ParseIP(cfg.Gateway)
	if gateway == nil || gateway.To4() == nil {
		return nil, fmt.Errorf("gateway %q is not an IPv4 address", cfg.Gateway)
	}

	payload := make([]byte, 0, 15)
	payload = append(payload, ip.To4()...)
	payload = append(payload, mask.To4()...)
	payload = append(payload, gateway.To4()...)
	flag := byte(0)
	if cfg.VLANEnabled {
		flag = 1
	}
	payload = append(payload, flag)
	payload = append(payload, byte(cfg.VLANID>>8), byte(cfg.VLANID))
	return payload, nil
}

// CommandError reports a lidar that answered a PTC request with a non-zero
// return code — a rejection, not a transport failure.
type CommandError struct {
	Cmd     byte
	RetCode byte
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("ptc: command %#04x rejected, return code %d", e.Cmd, e.RetCode)
}

// Client is a minimal PTC 1.0 client over one TCP connection.
type Client struct {
	conn    net.Conn
	timeout time.Duration
}

// Dial opens a PTC connection to addr (host:port).
func Dial(addr string, timeout time.Duration) (*Client, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, timeout: timeout}, nil
}

// Close releases the TCP connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// Exchange sends one command and reads the matching response, returning the
// response payload. Responses whose return code is not zero fail with a
// *CommandError; responses to another command fail with a plain error.
func (c *Client) Exchange(cmd byte, payload []byte) ([]byte, error) {
	if c.timeout > 0 {
		if err := c.conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
			return nil, err
		}
	}
	if _, err := c.conn.Write(BuildFrame(cmd, payload)); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	return readACK(c.conn, cmd)
}

// SetNet stores the given network configuration. A lidar only applies the
// stored configuration when it restarts: reboot afterwards (or power-cycle)
// for the change to take effect.
func (c *Client) SetNet(cfg NetConfig) error {
	payload, err := BuildSetNetPayload(cfg)
	if err != nil {
		return err
	}
	_, err = c.Exchange(CmdSetNet, payload)
	return err
}

// RebootLidar restarts the lidar — the cable-free way to apply a stored
// SetNet change. A JT128 comes back in about half a minute.
func (c *Client) RebootLidar() error {
	_, err := c.Exchange(CmdRebootLidar, nil)
	return err
}

// GetLidarCalibration reads the calibration file, the largest read-only
// answer a lidar gives; doubles as an end-to-end channel probe.
func (c *Client) GetLidarCalibration() ([]byte, error) {
	return c.Exchange(CmdGetLidarCalibration, nil)
}

// readACK reads one response frame for cmd, tolerating stray bytes before
// the frame marker exactly like the SDK's header search does.
func readACK(r io.Reader, cmd byte) ([]byte, error) {
	header := make([]byte, headerSize)
	if err := readHeader(r, header); err != nil {
		return nil, fmt.Errorf("read response header: %w", err)
	}
	if header[2] != cmd {
		return nil, fmt.Errorf("ptc: response command %#04x does not match request %#04x", header[2], cmd)
	}
	if rc := header[3]; rc != 0 {
		return nil, &CommandError{Cmd: cmd, RetCode: rc}
	}
	length := binary.BigEndian.Uint32(header[4:8])
	if length > maxACKPayload {
		return nil, fmt.Errorf("ptc: response announces %d payload bytes, over the %d limit", length, maxACKPayload)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("read %d payload bytes: %w", length, err)
	}
	return payload, nil
}

// readHeader fills header with the 8 bytes of a response frame. It first
// scans the stream one byte at a time until the 0x47 0x74 marker shows up —
// so stray bytes before the frame are skipped instead of desynchronizing the
// parser, exactly like the SDK's header search — and then reads the
// remaining six header bytes sequentially.
func readHeader(r io.Reader, header []byte) error {
	prev := byte(0)
	buf := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return err
		}
		if prev == headerIdent0 && buf[0] == headerIdent1 {
			break
		}
		prev = buf[0]
	}
	header[0], header[1] = headerIdent0, headerIdent1
	_, err := io.ReadFull(r, header[2:])
	return err
}
