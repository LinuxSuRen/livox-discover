// Command livox-discover set-ip: change the IP of a lidar.
//
// For Livox SDK2 lidars (the default) the change rides on the SDK2
// key-value control command (cmd 0x0100, key kKeyLidarIpCfg = 0x0004) which
// is unicast to the lidar's command port (56100 on the Mid360 family); the
// lidar ACKs with {ret_code, error_key}. For Hesai lidars (-vendor hesai)
// it rides on the PTC SetNet command (TCP 9347); the lidar ACKs its return
// code in the response header.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/hesai"
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
		fmt.Fprintf(fs.Output(), "usage: %s set-ip -lidar <current ip> -ip <new ip> [-mask m] [-gw g] [-vendor livox|hesai] [-cmd-port p] [-ptc-port p]\n", os.Args[0])
		fs.PrintDefaults()
	}
	lidarIP := fs.String("lidar", "", "current IP of the target lidar, as reported by livox-discover (required)")
	newIP := fs.String("ip", "", "new lidar IP (required)")
	mask := fs.String("mask", "255.255.255.0", "new subnet mask")
	gw := fs.String("gw", "", "new gateway; defaults to the first three octets of -ip with a final .1")
	vendor := fs.String("vendor", "livox", "lidar family: livox (SDK2 over UDP) or hesai (PTC over TCP)")
	cmdPort := fs.Int("cmd-port", defaultLidarCmdPort, "livox lidar command UDP port (56100 for Mid360/Mid360S/Avia2/Mid360L)")
	ptcPort := fs.Int("ptc-port", hesai.DefaultPTCPort, "hesai PTC TCP port (with -vendor hesai)")
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
	if *vendor != "livox" && *vendor != "hesai" {
		fmt.Fprintf(os.Stderr, "-vendor must be livox or hesai, got %q\n", *vendor)
		return 2
	}
	if *vendor == "livox" && (*cmdPort < 1 || *cmdPort > 65535) {
		fmt.Fprintln(os.Stderr, "-cmd-port must be 1-65535")
		return 2
	}
	if *vendor == "hesai" && (*ptcPort < 1 || *ptcPort > 65535) {
		fmt.Fprintln(os.Stderr, "-ptc-port must be 1-65535")
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

	targetIP := net.ParseIP(*lidarIP)
	if targetIP == nil || targetIP.To4() == nil {
		fmt.Fprintf(os.Stderr, "-lidar %q is not an IPv4 address\n", *lidarIP)
		return 2
	}

	if *vendor == "hesai" {
		cfg := hesai.NetConfig{IP: *newIP, Mask: *mask, Gateway: gateway}
		if _, err := hesai.BuildSetNetPayload(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "invalid target configuration: %v\n", err)
			return 2
		}
		fmt.Printf("[set-ip] %s:%d (PTC) -> ip=%s mask=%s gw=%s\n", *lidarIP, *ptcPort, cfg.IP, cfg.Mask, cfg.Gateway)
		client, err := dialHesai(targetIP, *ptcPort, func(format string, args ...any) {
			fmt.Printf(format+"\n", args...)
		})
		if err != nil {
			printPTCError("set-ip", err, *lidarIP)
			return 1
		}
		defer client.Close()
		if err := client.SetNet(cfg); err != nil {
			printPTCError("set-ip", err, *lidarIP)
			return 1
		}
		fmt.Printf("[set-ip] success: lidar %s stores ip %s mask %s gw %s\n", *lidarIP, cfg.IP, cfg.Mask, cfg.Gateway)
		fmt.Println("[set-ip] a stored change only takes effect after the lidar restarts, and the runtime IP stays on the old value until then.")
		fmt.Printf("[set-ip] apply it now: %s reboot -vendor hesai -lidar %s  (the reboot still targets the current IP)\n", os.Args[0], *lidarIP)
		return 0
	}

	cfg := livox.LidarIPConfig{IP: *newIP, Mask: *mask, Gateway: gateway}
	if _, err := livox.BuildSetLidarIPPayload(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "invalid target configuration: %v\n", err)
		return 2
	}

	fmt.Printf("[set-ip] %s:%d -> ip=%s mask=%s gw=%s\n", *lidarIP, *cmdPort, cfg.IP, cfg.Mask, cfg.Gateway)

	outcome := performSetIP(targetIP, *cmdPort, cfg, func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	})
	if outcome.err != nil {
		fmt.Fprintf(os.Stderr, "[set-ip] %v\n", outcome.err)
		if runtime.GOOS == "darwin" {
			fmt.Fprintf(os.Stderr, "[set-ip] lidar in another subnet? add a path first: sudo ifconfig <iface> alias <lidar-net>.50 255.255.255.0\n")
			fmt.Fprintf(os.Stderr, "[set-ip] or a host route only: sudo route -n add -host %s -interface <iface>\n", *lidarIP)
		} else {
			fmt.Fprintf(os.Stderr, "[set-ip] lidar in another subnet? add a path first: sudo ip addr add <lidar-net>.50/24 dev <iface>\n")
			fmt.Fprintf(os.Stderr, "[set-ip] or a host route only: sudo ip route add %s/32 dev <iface>\n", *lidarIP)
		}
		fmt.Fprintln(os.Stderr, "[set-ip] also check that -lidar is the lidar's current IP and that no driver occupies the lidar.")
		return 1
	}
	ack := *outcome.ack
	if ack.OK() {
		fmt.Printf("[ack] from %s  ret_code=0  error_key=0\n", outcome.from)
		fmt.Printf("[set-ip] success: lidar %s now uses ip %s mask %s gw %s\n", *lidarIP, cfg.IP, cfg.Mask, cfg.Gateway)
		fmt.Println("[set-ip] the change is stored and takes effect after the lidar restarts; run livox-discover reboot -lidar " + cfg.IP + " (or power-cycle) and verify.")
		return 0
	}
	fmt.Fprintf(os.Stderr, "[ack] from %s  ret_code=%d  error_key=%#04x\n", outcome.from, ack.RetCode, ack.ErrorKey)
	fmt.Fprintln(os.Stderr, "[set-ip] the lidar rejected the configuration; check that ip/mask/gw are consistent.")
	return 1
}

// readUntil waits up to wait for a datagram that parse accepts, skipping
// unrelated traffic such as the lidar's periodic status pushes, and reports
// the sender's address.
func readUntil[T any](conn net.PacketConn, buf []byte, wait time.Duration, parse func([]byte) (T, error)) (T, string, error) {
	var zero T
	if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		return zero, "", err
	}
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			return zero, "", err // deadline exceeded or socket closed
		}
		udp, ok := addr.(*net.UDPAddr)
		if !ok {
			continue
		}
		if v, err := parse(buf[:n]); err == nil {
			return v, udp.IP.String(), nil
		}
	}
}

// readACK waits up to wait for the key-value control ACK matching seq.
func readACK(conn net.PacketConn, buf []byte, seq uint32, wait time.Duration) (livox.AsyncControlACK, string, error) {
	return readUntil(conn, buf, wait, func(data []byte) (livox.AsyncControlACK, error) {
		ack, _, err := livox.ParseAsyncControlACK(data, seq)
		return ack, err
	})
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
