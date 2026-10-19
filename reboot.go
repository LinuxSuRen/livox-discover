// Command livox-discover reboot: restart a lidar through the SDK2 reboot
// command (cmd 0x0200) so saved configuration — for example a new IP from
// set-ip, which only takes effect across a restart — applies without
// touching the power cable.
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
	rebootAttempts    = 3
	rebootAttemptWait = time.Second
)

// runReboot implements the reboot subcommand. Exit codes: 0 the lidar
// accepted, 1 no or rejecting ack, 2 bad usage.
func runReboot(args []string) int {
	fs := flag.NewFlagSet("reboot", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: %s reboot -lidar <ip> [-cmd-port p] [-delay d]\n", os.Args[0])
		fs.PrintDefaults()
	}
	lidarIP := fs.String("lidar", "", "current IP of the target lidar (required)")
	cmdPort := fs.Int("cmd-port", defaultLidarCmdPort, "lidar command UDP port (56100 for the Mid360 family)")
	delay := fs.Uint("delay", uint(livox.DefaultRebootDelay), "reboot delay the lidar applies before restarting")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q (see: reboot -help)\n", fs.Arg(0))
		return 2
	}
	if *lidarIP == "" {
		fmt.Fprintln(os.Stderr, "-lidar is required")
		return 2
	}
	if *cmdPort < 1 || *cmdPort > 65535 {
		fmt.Fprintln(os.Stderr, "-cmd-port must be 1-65535")
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
	fmt.Printf("[reboot] %s:%d  delay=%d  (from :%d)\n", *lidarIP, *cmdPort, *delay, localPort)

	buf := make([]byte, recvBufSize)
	for attempt := 1; attempt <= rebootAttempts; attempt++ {
		seq := uint32(attempt)
		frame, err := livox.BuildRebootFrame(uint32(*delay), seq)
		if err != nil {
			fmt.Fprintf(os.Stderr, "build frame: %v\n", err)
			return 1
		}
		if _, err := conn.WriteTo(frame, target); err != nil {
			fmt.Fprintf(os.Stderr, "send: %v\n", err)
			return 1
		}
		fmt.Printf("[reboot] attempt %d/%d: sent %d bytes\n", attempt, rebootAttempts, len(frame))

		ack, from, err := readUntil(conn, buf, rebootAttemptWait, func(data []byte) (livox.RebootACK, error) {
			return livox.ParseRebootACK(data, seq)
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "[reboot] attempt %d/%d: %v\n", attempt, rebootAttempts, err)
			continue
		}
		if !ack.OK() {
			fmt.Fprintf(os.Stderr, "[ack] from %s  ret_code=%d\n", from, ack.RetCode)
			fmt.Fprintln(os.Stderr, "[reboot] the lidar rejected the reboot request.")
			return 1
		}
		fmt.Printf("[ack] from %s  ret_code=0\n", from)
		fmt.Printf("[reboot] success: %s is restarting; a Mid360 boots in well under a minute.\n", *lidarIP)
		fmt.Println("[reboot] afterwards run livox-discover again (or query the new IP directly) to verify.")
		return 0
	}
	fmt.Fprintf(os.Stderr, "[reboot] no ack from %s:%d after %d attempts; check the IP and subnet reachability.\n",
		*lidarIP, *cmdPort, rebootAttempts)
	return 1
}
