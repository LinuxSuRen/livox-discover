// Command livox-discover set-ip -vendor hesai and reboot -vendor hesai:
// manage a Hesai lidar (JT128 / AT128 family) through PTC, the TCP control
// channel its SDK speaks, instead of the Livox SDK2 UDP command channel.
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/hesai"
)

const (
	// ptcTimeout bounds one PTC dial or command exchange; generous on
	// purpose because the calibration probe answer is tens of KiB.
	ptcTimeout = 5 * time.Second
)

// dialHesai opens a PTC connection to lidarIP:ptcPort and proves it with the
// read-only calibration query, so a dead or half-open channel is reported as
// such before any configuration is written.
func dialHesai(lidarIP net.IP, ptcPort int, logf func(string, ...any)) (*hesai.Client, error) {
	client, err := hesai.Dial(fmt.Sprintf("%s:%d", lidarIP, ptcPort), ptcTimeout)
	if err != nil {
		return nil, err
	}
	cal, err := client.GetLidarCalibration()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("ptc channel probe failed: %w", err)
	}
	if logf != nil {
		logf("[ptc] channel ok (calibration answer: %d bytes)", len(cal))
	}
	return client, nil
}

// printPTCError explains a failed PTC operation; the cross-subnet advice
// matches the Livox path because the symptom is the same.
func printPTCError(op string, err error, lidarIP string) {
	var cmdErr *hesai.CommandError
	if errors.As(err, &cmdErr) {
		fmt.Fprintf(os.Stderr, "[%s] the lidar rejected the request: %v\n", op, cmdErr)
		return
	}
	fmt.Fprintf(os.Stderr, "[%s] %v\n", op, err)
	if runtime.GOOS == "darwin" {
		fmt.Fprintf(os.Stderr, "[%s] lidar in another subnet? add a path first: sudo ifconfig <iface> alias <lidar-net>.50 255.255.255.0\n", op)
		fmt.Fprintf(os.Stderr, "[%s] or a host route only: sudo route -n add -host %s -interface <iface>\n", op, lidarIP)
	} else {
		fmt.Fprintf(os.Stderr, "[%s] lidar in another subnet? add a path first: sudo ip addr add <lidar-net>.50/24 dev <iface>\n", op)
		fmt.Fprintf(os.Stderr, "[%s] or a host route only: sudo ip route add %s/32 dev <iface>\n", op, lidarIP)
	}
	fmt.Fprintf(os.Stderr, "[%s] also check that %s is the lidar's current IP (try: %s -vendor hesai).\n", op, lidarIP, os.Args[0])
}
