// Command livox-discover finds Livox SDK2 lidars on the local network by
// listening to the UDP ports they broadcast or push to, and reports every
// sender it observes. Datagrams that decode as SDK2 discovery ACKs are
// surfaced with device type, serial number and lidar IP. With -probe a
// discovery query is actively broadcast from every IPv4 interface as well,
// which also reaches lidars that stay silent unless queried. With
// -vendor hesai the MSOP port (2368) is added to the listeners, so Hesai
// lidars (JT128 / AT128 family) show up through the point clouds they
// stream.
//
// The set-ip subcommand changes a lidar's own IP, subnet mask and gateway —
// through the SDK2 key-value control command for Livox, or through PTC (TCP
// 9347) for Hesai; reboot restarts a lidar so a saved change takes effect
// without touching its power; web serves a small browser UI wrapping the
// Livox capabilities.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/hesai"
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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "set-ip":
			return runSetIP(os.Args[2:])
		case "reboot":
			return runReboot(os.Args[2:])
		case "web":
			return runWeb(os.Args[2:])
		}
	}

	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	duration := fs.Duration("duration", defaultDuration, "how long to listen, e.g. 15s, 500ms, 2m")
	jsonOut := fs.Bool("json", false, "print a machine-readable JSON report instead of the text log")
	portsFlag := fs.String("ports", defaultPorts, "comma-separated UDP ports to listen on")
	probe := fs.Bool("probe", false, "also broadcast a discovery query every second from every IPv4 interface (finds lidars that only answer queries)")
	vendorFlag := fs.String("vendor", "livox", "comma-separated lidar families to listen for: livox, hesai (hesai adds the 2368 MSOP port)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q (use flags only, or the set-ip / reboot subcommands; see -help)\n", fs.Arg(0))
		return 2
	}
	if *duration <= 0 {
		fmt.Fprintln(os.Stderr, "-duration must be positive")
		return 2
	}
	vendors, err := parseVendors(*vendorFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ports, err := parsePorts(*portsFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if vendors["hesai"] && !containsPort(ports, hesai.MSOPPort) {
		ports = append(ports, hesai.MSOPPort)
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

	if msg := lidarSidePortWarning(ports); msg != "" {
		logf("%s", msg)
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

	c, interrupted := listenAndCollect(ctx, bound, *duration, *jsonOut, logf, *probe, nil)
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
// cancelled, and returns the aggregated collector. With probe set it also
// broadcasts discovery queries from every IPv4 interface while listening.
// onTick, when not nil, is called about once per second with the collector,
// on the same goroutine, so callers can snapshot progress race-free.
func listenAndCollect(ctx context.Context, bound []boundPort, duration time.Duration, jsonOut bool, logf func(string, ...any), probe bool, onTick func(*collector)) (*collector, bool) {
	logf("listening %s ...", duration)
	timer := time.NewTimer(duration)
	defer timer.Stop()
	var tick <-chan time.Time
	if onTick != nil {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		tick = ticker.C
	}

	packets := make(chan udpPacket, 256)
	var wg sync.WaitGroup
	for _, b := range bound {
		wg.Add(1)
		go readLoop(&wg, packets, b)
	}
	// ownIPs carries this host's own addresses so the loopback copies of
	// our own probe broadcasts are not mistaken for lidar traffic.
	ownIPs := map[string]bool{}
	var stopProbe func()
	if probe {
		var probeConn net.PacketConn
		for _, b := range bound {
			if b.port == discoveryQueryPort {
				probeConn = b.conn
			}
		}
		stopProbe, ownIPs = startProbes(probeConn, logf)
	}

	c := newCollector()
collect:
	for {
		select {
		case p := <-packets:
			if !ownIPs[p.srcIP] {
				handlePacket(c, p, jsonOut)
			}
		case <-tick:
			onTick(c)
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
	if stopProbe != nil {
		stopProbe()
	}
	go func() {
		wg.Wait()
		close(packets)
	}()
	for p := range packets {
		if !ownIPs[p.srcIP] {
			handlePacket(c, p, jsonOut)
		}
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
	if ev.NewHesai {
		fmt.Printf("[hesai] src=%s:%d -> local:%d  len=%d  Hesai MSOP point cloud — the src IP is the lidar's address\n",
			p.srcIP, p.srcPort, p.dstPort, len(p.data))
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
		if s.HesaiPackets > 0 {
			fmt.Printf("    hesai_msop=%d  Hesai MSOP point cloud; the src IP is the lidar's address\n", s.HesaiPackets)
		}
		for _, d := range s.sortedDiscoveries() {
			fmt.Printf("    dev=%s(%d)  sn=%s  lidar_ip=%s  cmd_port=%d  ret_code=%d\n",
				d.DevTypeName(), d.DevType, d.SN, d.LidarIP, d.CmdPort, d.RetCode)
		}
	}
	for _, h := range crossSubnetHints(c.allDiscoveries(), localIPv4Nets(), runtime.GOOS) {
		fmt.Println(h)
	}
	var hesaiIPs []string
	for _, s := range sources {
		if s.HesaiPackets == 0 {
			continue
		}
		if ip := net.ParseIP(s.IP); ip != nil && ip.IsLoopback() {
			continue // local test senders are not a routing problem
		}
		hesaiIPs = append(hesaiIPs, s.IP)
	}
	for _, h := range crossSubnetHintLines(hesaiIPs, localIPv4Nets(), runtime.GOOS) {
		fmt.Println(h)
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

// parseVendors splits a comma-separated -vendor list into a set, rejecting
// unknown families so a typo cannot silently narrow a scan to nothing.
func parseVendors(spec string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, field := range strings.Split(spec, ",") {
		field = strings.ToLower(strings.TrimSpace(field))
		if field == "" {
			continue
		}
		switch field {
		case "livox", "hesai":
			out[field] = true
		default:
			return nil, fmt.Errorf("unknown vendor %q (want livox and/or hesai)", field)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no vendor in %q", spec)
	}
	return out, nil
}

// containsPort reports whether ports lists p.
func containsPort(ports []int, p int) bool {
	for _, port := range ports {
		if port == p {
			return true
		}
	}
	return false
}

// lidarSidePorts lists the lidar-side command/data ports of the SDK2 Mid360
// family next to the host-side ports the lidar actually sends to (from
// comm/define.h of Livox-SDK2: lidar 56x00, host 56x01). Listening on a
// lidar-side port is a common mix-up that keeps the tool silent forever.
var lidarSidePorts = []struct{ lidar, host int }{
	{56100, 56101}, // command channel
	{56200, 56201}, // status push
	{56300, 56301}, // point cloud
	{56400, 56401}, // IMU
	{56500, 56501}, // log
}

// lidarSidePortWarning returns a warning line when ports contains lidar-side
// ports, or "" when every port is a legitimate listening target.
func lidarSidePortWarning(ports []int) string {
	contains := make(map[int]bool, len(ports))
	for _, p := range ports {
		contains[p] = true
	}
	var lidarSide, hostSide []string
	for _, pair := range lidarSidePorts {
		if contains[pair.lidar] {
			lidarSide = append(lidarSide, strconv.Itoa(pair.lidar))
			hostSide = append(hostSide, strconv.Itoa(pair.host))
		}
	}
	if len(lidarSide) == 0 {
		return ""
	}
	verb := "are lidar-side ports"
	if len(lidarSide) == 1 {
		verb = "is a lidar-side port"
	}
	return fmt.Sprintf("[warn] %s %s: the lidar listens there but never sends; "+
		"to observe its traffic listen on %s instead (discovery broadcasts arrive on 56000)",
		strings.Join(lidarSide, ","), verb, strings.Join(hostSide, ","))
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
