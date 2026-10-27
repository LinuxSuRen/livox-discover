// Command livox-discover reboot: restart a lidar so saved configuration —
// for example a new IP from set-ip, which only takes effect across a
// restart — applies without touching the power cable. Livox lidars get the
// SDK2 reboot command (cmd 0x0200) over UDP; Hesai lidars (-vendor hesai)
// get the PTC reboot command over TCP.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/hesai"
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
		fmt.Fprintf(fs.Output(), "usage: %s reboot -lidar <ip> [-vendor livox|hesai] [-cmd-port p] [-ptc-port p] [-delay d]\n", os.Args[0])
		fs.PrintDefaults()
	}
	lidarIP := fs.String("lidar", "", "current IP of the target lidar (required)")
	vendor := fs.String("vendor", "livox", "lidar family: livox (SDK2 over UDP) or hesai (PTC over TCP)")
	cmdPort := fs.Int("cmd-port", defaultLidarCmdPort, "livox lidar command UDP port (56100 for the Mid360 family)")
	ptcPort := fs.Int("ptc-port", hesai.DefaultPTCPort, "hesai PTC TCP port (with -vendor hesai)")
	delay := fs.Uint("delay", uint(livox.DefaultRebootDelay), "reboot delay the lidar applies before restarting (livox only)")
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
	targetIP := net.ParseIP(*lidarIP)
	if targetIP == nil || targetIP.To4() == nil {
		fmt.Fprintf(os.Stderr, "-lidar %q is not an IPv4 address\n", *lidarIP)
		return 2
	}

	if *vendor == "hesai" {
		fmt.Printf("[reboot] %s:%d (PTC)\n", *lidarIP, *ptcPort)
		client, err := dialHesai(targetIP, *ptcPort, func(format string, args ...any) {
			fmt.Printf(format+"\n", args...)
		})
		if err != nil {
			printPTCError("reboot", err, *lidarIP)
			return 1
		}
		defer client.Close()
		if err := client.RebootLidar(); err != nil {
			printPTCError("reboot", err, *lidarIP)
			return 1
		}
		fmt.Printf("[reboot] success: %s is restarting; a JT128 comes back in about half a minute, streaming from the IP it has stored.\n", *lidarIP)
		fmt.Printf("[reboot] afterwards run %s -vendor hesai again to see which address the point clouds come from now.\n", os.Args[0])
		return 0
	}

	fmt.Printf("[reboot] %s:%d  delay=%d\n", *lidarIP, *cmdPort, *delay)

	outcome := performReboot(targetIP, *cmdPort, uint32(*delay), func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	})
	if outcome.err != nil {
		fmt.Fprintf(os.Stderr, "[reboot] %v; check the IP and subnet reachability.\n", outcome.err)
		return 1
	}
	ack := *outcome.ack
	if !ack.OK() {
		fmt.Fprintf(os.Stderr, "[ack] from %s  ret_code=%d\n", outcome.from, ack.RetCode)
		fmt.Fprintln(os.Stderr, "[reboot] the lidar rejected the reboot request.")
		return 1
	}
	fmt.Printf("[ack] from %s  ret_code=0\n", outcome.from)
	fmt.Printf("[reboot] success: %s is restarting; a Mid360 boots in well under a minute.\n", *lidarIP)
	fmt.Println("[reboot] afterwards run livox-discover again (or query the new IP directly) to verify.")
	return 0
}
