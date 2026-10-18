// Command livox-discover set-ip: change the IP of a Livox SDK2 lidar.
//
// The change rides on the SDK2 key-value control command (cmd 0x0100, key
// kKeyLidarIpCfg = 0x0004) which is unicast to the lidar's command port
// (56100 on the Mid360 family); the lidar ACKs with {ret_code, error_key}.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/livox"
)

const (
	// setIPAttempts is how often the command is repeated when the lidar
	// stays silent; one lost datagram must not fail the change.
	setIPAttempts = 3
	// setIPAttemptWait mirrors the SDK's 1s command timeout per attempt.
	setIPAttemptWait = time.Second
	// preferredHostCmdPort is the host-side command port the SDK binds
	// (kMid360HostCmdPort); ACKs are read back on it.
	preferredHostCmdPort = 56101
	// defaultLidarCmdPort is the lidar-side command port of the Mid360
	// family (kMid360LidarCmdPort).
	defaultLidarCmdPort = 56100
)

// runSetIP implements the set-ip subcommand. Exit codes follow the tool's
// convention: 0 changed, 1 no/rejecting ack, 2 bad usage.
func runSetIP(args []string) int {
	fs := flag.NewFlagSet("set-ip", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: %s set-ip -lidar <current ip> -ip <new ip> [-mask m] [-gw g] [-cmd-port p]\n", os.Args[0])
		fs.PrintDefaults()
	}
	lidarIP := fs.String("lidar", "", "current IP of the target lidar, as reported by livox-discover (required)")
	newIP := fs.String("ip", "", "new lidar IP (required)")
	mask := fs.String("mask", "255.255.255.0", "new subnet mask")
	gw := fs.String("gw", "", "new gateway; defaults to the first three octets of -ip with a final .1")
	cmdPort := fs.Int("cmd-port", defaultLidarCmdPort, "lidar command UDP port (56100 for Mid360/Mid360S/Avia2/Mid360L)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q (see: set-ip -help)\n", fs.Arg(0))
		return 2
	}
	if *lidarIP == "" || *newIP == "" {
		fmt.Fprintln(os.Stderr, "-lidar and -ip are required")
		return 2
	}
	if *cmdPort < 1 || *cmdPort > 65535 {
		fmt.Fprintln(os.Stderr, "-cmd-port must be 1-65535")
		return 2
	}

	gateway := *gw
	if gateway == "" {
		var err error
		if gateway, err = deriveGateway(*newIP); err != nil {
			fmt.Fprintf(os.Stderr, "cannot derive gateway from -ip: %v (pass -gw explicitly)\n", err)
			return 2
		}
	}

	cfg := livox.LidarIPConfig{IP: *newIP, Mask: *mask, Gateway: gateway}
	if _, err := livox.BuildSetLidarIPPayload(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "invalid target configuration: %v\n", err)
		return 2
	}
	targetIP := net.ParseIP(*lidarIP)
	if targetIP == nil || targetIP.To4() == nil {
		fmt.Fprintf(os.Stderr, "-lidar %q is not an IPv4 address\n", *lidarIP)
		return 2
	}

	conn, localPort, err := openCmdSocket()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open a UDP socket: %v\n", err)
		return 1
	}
	defer conn.Close()
	if localPort != preferredHostCmdPort {
		fmt.Fprintf(os.Stderr, "[warn] port %d busy, sending from ephemeral port %d instead\n", preferredHostCmdPort, localPort)
	}

	target := &net.UDPAddr{IP: targetIP.To4(), Port: *cmdPort}
	fmt.Printf("[set-ip] %s:%d -> ip=%s mask=%s gw=%s (from :%d)\n",
		*lidarIP, *cmdPort, cfg.IP, cfg.Mask, cfg.Gateway, localPort)

	buf := make([]byte, recvBufSize)
	for attempt := 1; attempt <= setIPAttempts; attempt++ {
		seq := uint32(attempt)
		frame, err := livox.BuildSetLidarIPFrame(cfg, seq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "build frame: %v\n", err)
			return 1
		}
		if _, err := conn.WriteTo(frame, target); err != nil {
			fmt.Fprintf(os.Stderr, "send: %v\n", err)
			return 1
		}
		fmt.Printf("[set-ip] attempt %d/%d: sent %d bytes\n", attempt, setIPAttempts, len(frame))

		ack, from, err := readACK(conn, buf, seq, setIPAttemptWait)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[set-ip] attempt %d/%d: %v\n", attempt, setIPAttempts, err)
			continue
		}
		if ack.OK() {
			fmt.Printf("[ack] from %s  ret_code=0  error_key=0\n", from)
			fmt.Printf("[set-ip] success: lidar %s now uses ip %s mask %s gw %s\n", *lidarIP, cfg.IP, cfg.Mask, cfg.Gateway)
			fmt.Println("[set-ip] verify with a fresh discovery run; the lidar should now answer from its new IP.")
			fmt.Println("[set-ip] if it stays silent, power-cycle the lidar once and re-run livox-discover.")
			return 0
		}
		fmt.Fprintf(os.Stderr, "[ack] from %s  ret_code=%d  error_key=%#04x\n",
			from, ack.RetCode, ack.ErrorKey)
		fmt.Fprintln(os.Stderr, "[set-ip] the lidar rejected the configuration; check that ip/mask/gw are consistent.")
		return 1
	}
	fmt.Fprintf(os.Stderr, "[set-ip] no ack from %s:%d after %d attempts\n", *lidarIP, *cmdPort, setIPAttempts)
	fmt.Fprintln(os.Stderr, "[set-ip] check that -lidar is the lidar's current IP, that no driver occupies the")
	fmt.Fprintln(os.Stderr, "[set-ip] lidar, and that this host shares the lidar's subnet.")
	return 1
}

// readACK waits up to wait for the ACK matching seq, skipping unrelated
// datagrams (e.g. the lidar's periodic discovery broadcasts).
func readACK(conn net.PacketConn, buf []byte, seq uint32, wait time.Duration) (livox.AsyncControlACK, string, error) {
	if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		return livox.AsyncControlACK{}, "", err
	}
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return livox.AsyncControlACK{}, "", err // deadline exceeded or socket closed
		}
		udp, ok := addr.(*net.UDPAddr)
		if !ok {
			continue
		}
		if ack, _, err := livox.ParseAsyncControlACK(buf[:n], seq); err == nil {
			return ack, udp.IP.String(), nil
		}
	}
}

// openCmdSocket binds the host command port the SDK uses so the lidar's ACK
// arrives where a driver would expect it; when the port is taken it falls
// back to an ephemeral port, which the lidar answers just as well.
func openCmdSocket() (net.PacketConn, int, error) {
	if conn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", preferredHostCmdPort)); err == nil {
		return conn, preferredHostCmdPort, nil
	}
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, 0, err
	}
	return conn, conn.LocalAddr().(*net.UDPAddr).Port, nil
}

// deriveGateway returns the default gateway for -gw: the first three octets
// of the new IP with a final .1, matching Livox's factory convention
// (192.168.1.1xx / 255.255.255.0 / 192.168.1.1).
func deriveGateway(ip string) (string, error) {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return "", fmt.Errorf("%q is not an IPv4 address", ip)
	}
	octets := parsed.To4()
	return fmt.Sprintf("%d.%d.%d.1", octets[0], octets[1], octets[2]), nil
}
