// Command exchanges shared by the CLI subcommands and the web API: each
// perform* function drives one full lidar command with retries and reports
// the raw outcome, while callers decide how to present it.
package main

import (
	"fmt"
	"net"

	"github.com/LinuxSuRen/livox-discover/internal/livox"
)

// setIPOutcome is the result of a complete set-ip exchange.
type setIPOutcome struct {
	ack       *livox.AsyncControlACK // non-nil when the lidar answered
	from      string                 // address that answered
	localPort int                    // host port the command was sent from
	err       error                  // socket, send or no-ack failure
}

// performSetIP runs the kKeyLidarIpCfg command with retries against
// lidarIP:cmdPort. logf, when not nil, receives per-attempt progress.
func performSetIP(lidarIP net.IP, cmdPort int, cfg livox.LidarIPConfig, logf func(string, ...any)) setIPOutcome {
	conn, localPort, err := openCmdSocket()
	if err != nil {
		return setIPOutcome{err: err}
	}
	defer conn.Close()
	if logf != nil && localPort != preferredHostCmdPort {
		logf("[warn] port %d busy, sending from ephemeral port %d instead", preferredHostCmdPort, localPort)
	}

	target := &net.UDPAddr{IP: lidarIP.To4(), Port: cmdPort}
	buf := make([]byte, recvBufSize)
	for attempt := 1; attempt <= setIPAttempts; attempt++ {
		seq := uint32(attempt)
		frame, err := livox.BuildSetLidarIPFrame(cfg, seq)
		if err != nil {
			return setIPOutcome{err: err, localPort: localPort}
		}
		if _, err := conn.WriteTo(frame, target); err != nil {
			return setIPOutcome{err: fmt.Errorf("send: %w", err), localPort: localPort}
		}
		if logf != nil {
			logf("[set-ip] attempt %d/%d: sent %d bytes", attempt, setIPAttempts, len(frame))
		}

		ack, from, err := readACK(conn, buf, seq, setIPAttemptWait)
		if err != nil {
			if logf != nil {
				logf("[set-ip] attempt %d/%d: %v", attempt, setIPAttempts, err)
			}
			continue
		}
		return setIPOutcome{ack: &ack, from: from, localPort: localPort}
	}
	return setIPOutcome{
		err:       fmt.Errorf("no ack from %s:%d after %d attempts", lidarIP, cmdPort, setIPAttempts),
		localPort: localPort,
	}
}

// rebootOutcome is the result of a complete reboot exchange.
type rebootOutcome struct {
	ack       *livox.RebootACK // non-nil when the lidar answered
	from      string
	localPort int
	err       error
}

// performReboot runs the reboot command with retries against lidarIP:cmdPort.
func performReboot(lidarIP net.IP, cmdPort int, delay uint32, logf func(string, ...any)) rebootOutcome {
	conn, localPort, err := openCmdSocket()
	if err != nil {
		return rebootOutcome{err: err}
	}
	defer conn.Close()
	if logf != nil && localPort != preferredHostCmdPort {
		logf("[warn] port %d busy, sending from ephemeral port %d instead", preferredHostCmdPort, localPort)
	}

	target := &net.UDPAddr{IP: lidarIP.To4(), Port: cmdPort}
	buf := make([]byte, recvBufSize)
	for attempt := 1; attempt <= rebootAttempts; attempt++ {
		seq := uint32(attempt)
		frame, err := livox.BuildRebootFrame(delay, seq)
		if err != nil {
			return rebootOutcome{err: err, localPort: localPort}
		}
		if _, err := conn.WriteTo(frame, target); err != nil {
			return rebootOutcome{err: fmt.Errorf("send: %w", err), localPort: localPort}
		}
		if logf != nil {
			logf("[reboot] attempt %d/%d: sent %d bytes", attempt, rebootAttempts, len(frame))
		}

		ack, from, err := readUntil(conn, buf, rebootAttemptWait, func(data []byte) (livox.RebootACK, error) {
			return livox.ParseRebootACK(data, seq)
		})
		if err != nil {
			if logf != nil {
				logf("[reboot] attempt %d/%d: %v", attempt, rebootAttempts, err)
			}
			continue
		}
		return rebootOutcome{ack: &ack, from: from, localPort: localPort}
	}
	return rebootOutcome{
		err:       fmt.Errorf("no ack from %s:%d after %d attempts", lidarIP, cmdPort, rebootAttempts),
		localPort: localPort,
	}
}
