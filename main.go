// Command livox-discover finds Livox SDK2 lidars on the local network by
// listening to the UDP ports they broadcast or push to, and reports every
// sender it observes. Datagrams that decode as SDK2 discovery ACKs are
// surfaced with device type, serial number and lidar IP.
//
// The set-ip subcommand additionally changes a lidar's own IP, subnet mask
// and gateway through the SDK2 key-value control command.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultDuration = 15 * time.Second
	defaultPorts    = "55000,56000,56201"
	recvBufSize     = 2048
	// interruptedExitCode is the conventional exit code for a run cut short
	// by SIGINT/SIGTERM; the report is still printed before exiting.
	interruptedExitCode = 130
)

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) > 1 && os.Args[1] == "set-ip" {
		return runSetIP(os.Args[2:])
	}

	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	duration := fs.Duration("duration", defaultDuration, "how long to listen, e.g. 15s, 500ms, 2m")
	jsonOut := fs.Bool("json", false, "print a machine-readable JSON report instead of the text log")
	portsFlag := fs.String("ports", defaultPorts, "comma-separated UDP ports to listen on")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q (use flags only, or the set-ip subcommand; see -help)\n", fs.Arg(0))
		return 2
	}
	if *duration <= 0 {
		fmt.Fprintln(os.Stderr, "-duration must be positive")
		return 2
	}
	ports, err := parsePorts(*portsFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	// In text mode progress goes to stdout like the original tool; in JSON
	// mode stdout carries only the final report.
	logf := func(format string, args ...any) {
		fmt.Printf(format+"\n", args...)
	}
	if *jsonOut {
		logf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}

	bound, skipped := openListeners(ports)
	var skippedJSON []skippedPortJSON
	for _, port := range ports {
		if bindErr, ok := skipped[port]; ok {
			logf("[skip]   port %d: %v", port, bindErr)
			skippedJSON = append(skippedJSON, skippedPortJSON{Port: port, Error: bindErr.Error()})
		} else {
			logf("[listen] 0.0.0.0:%d", port)
		}
	}
	if len(bound) == 0 {
		if *jsonOut {
			emitJSON(reportJSON{Ports: ports, Skipped: skippedJSON, Sources: []sourceJSON{}})
			fmt.Fprintln(os.Stderr, "no ports available")
		} else {
			fmt.Println("no ports available")
		}
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c, interrupted := listenAndCollect(ctx, bound, *duration, *jsonOut, logf)
	if *jsonOut {
		emitJSON(buildReport(c, *duration, ports, skippedJSON, interrupted))
	} else {
		printSummary(c)
	}
	if interrupted {
		return interruptedExitCode
	}
	return 0
}

// listenAndCollect reads packets until the duration elapses or the context is
// cancelled, and returns the aggregated collector.
func listenAndCollect(ctx context.Context, bound []boundPort, duration time.Duration, jsonOut bool, logf func(string, ...any)) (*collector, bool) {
	logf("listening %s ...", duration)
	timer := time.NewTimer(duration)
	defer timer.Stop()

	packets := make(chan udpPacket, 256)
	var wg sync.WaitGroup
	for _, b := range bound {
		wg.Add(1)
		go readLoop(&wg, packets, b)
	}

	c := newCollector()
collect:
	for {
		select {
		case p := <-packets:
			handlePacket(c, p, jsonOut)
		case <-timer.C:
			break collect
		case <-ctx.Done():
			break collect
		}
	}

	// Unblock the readers, then keep draining until they have all exited so
	// no in-flight packet is lost before the summary is printed.
	for _, b := range bound {
		b.conn.Close()
	}
	go func() {
		wg.Wait()
		close(packets)
	}()
	for p := range packets {
		handlePacket(c, p, jsonOut)
	}
	return c, ctx.Err() != nil
}

func handlePacket(c *collector, p udpPacket, jsonOut bool) {
	ev := c.add(p.srcIP, p.srcPort, p.dstPort, p.data)
	if jsonOut {
		return
	}
	if ev.ShowHit {
		fmt.Printf("[hit] src=%s:%d -> local:%d  len=%d  head=%s  cnt=%d\n",
			p.srcIP, p.srcPort, p.dstPort, len(p.data), hexHead(p.data, 16), ev.HitCount)
	}
	if d := ev.NewDiscovery; d != nil {
		fmt.Printf("[discovery] src=%s  dev=%s(%d)  sn=%s  lidar_ip=%s  cmd_port=%d\n",
			p.srcIP, d.DevTypeName(), d.DevType, d.SN, d.LidarIP, d.CmdPort)
	}
}

func printSummary(c *collector) {
	fmt.Println("=== summary ===")
	sources := c.sortedSources()
	if len(sources) == 0 {
		fmt.Println("no packets received; check that the lidar is powered on and on the same subnet")
		return
	}
	for _, s := range sources {
		fmt.Printf("src %-15s packets=%-6d livox_header=%-6d ports=%s\n",
			s.IP, s.Packets, s.LivoxPackets, formatPortCounts(s.Ports))
		for _, d := range s.sortedDiscoveries() {
			fmt.Printf("    dev=%s(%d)  sn=%s  lidar_ip=%s  cmd_port=%d  ret_code=%d\n",
				d.DevTypeName(), d.DevType, d.SN, d.LidarIP, d.CmdPort, d.RetCode)
		}
	}
}

func emitJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "json encode: %v\n", err)
		return
	}
	fmt.Println(string(b))
}

func parsePorts(spec string) ([]int, error) {
	seen := make(map[int]bool)
	var ports []int
	for _, field := range strings.Split(spec, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		port, err := strconv.Atoi(field)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid port %q (want 1-65535)", field)
		}
		if seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("no valid ports in %q", spec)
	}
	return ports, nil
}

// hexHead renders up to n leading bytes as space-separated hex.
func hexHead(data []byte, n int) string {
	if len(data) > n {
		data = data[:n]
	}
	parts := make([]string, len(data))
	for i, b := range data {
		parts[i] = fmt.Sprintf("%02x", b)
	}
	return strings.Join(parts, " ")
}

type udpPacket struct {
	srcIP   string
	srcPort int
	dstPort int
	data    []byte
}

type boundPort struct {
	port int
	conn net.PacketConn
}

var listenConfig = net.ListenConfig{
	Control: controlReuseAddr,
}

func openListeners(ports []int) (bound []boundPort, skipped map[int]error) {
	skipped = make(map[int]error)
	for _, port := range ports {
		conn, err := listenConfig.ListenPacket(context.Background(), "udp4", fmt.Sprintf(":%d", port))
		if err != nil {
			skipped[port] = err
			continue
		}
		bound = append(bound, boundPort{port: port, conn: conn})
	}
	return bound, skipped
}

// readLoop forwards datagrams from one bound port until its listener is
// closed. data is copied because the receive buffer is reused.
func readLoop(wg *sync.WaitGroup, packets chan<- udpPacket, b boundPort) {
	defer wg.Done()
	buf := make([]byte, recvBufSize)
	for {
		n, addr, err := b.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		udp, ok := addr.(*net.UDPAddr)
		if !ok {
			continue
		}
		packets <- udpPacket{
			srcIP:   udp.IP.String(),
			srcPort: udp.Port,
			dstPort: b.port,
			data:    append([]byte(nil), buf[:n]...),
		}
	}
}
