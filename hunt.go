// Command livox-discover hunt: find lidars whose IP is unknown by sweeping
// unicast discovery queries over whole CIDR blocks.
//
// Some lidar/firmware combinations never send spontaneous broadcasts and
// ignore discovery queries from other subnets (verified on Mid360S units),
// which makes both passive listening and -probe blind: a directly connected
// lidar with an unknown IP in a foreign subnet is otherwise undetectable.
// Hunt takes the opposite approach: it unicasts a query to every host of the
// given CIDR blocks — the lidar answers the one packet addressed to itself.
//
// The CIDR must be on-link for the sweep to reach the wire; when nothing is
// found, the tool prints the route command that makes it on-link.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/livox"
)

const (
	// huntDefaultCIDRs covers the whole 192.168.0.0/16 space where Livox
	// factory defaults (192.168.1.1xx for Mid360S, 192.168.144.1xx for
	// Mid360) and most robot-internal networks live.
	huntDefaultCIDRs = "192.168.0.0/16"
	// huntRounds repeats the sweep so a single lost reply does not miss a
	// lidar.
	huntRounds = 2
	// huntSubnetPause paces the sweep at roughly one /24 per 40ms to keep
	// the kernel's ARP machinery from throttling.
	huntSubnetPause = 40 * time.Millisecond
	// huntFinalWait is how long replies are collected after the last sweep.
	huntFinalWait = 2 * time.Second
)

// runHunt implements the hunt subcommand. Exit codes: 0 at least one lidar
// answered, 1 nothing found, 2 bad usage.
func runHunt(args []string) int {
	fs := flag.NewFlagSet("hunt", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: %s hunt [-cidr 192.168.0.0/16]\n", os.Args[0])
		fs.PrintDefaults()
	}
	cidrFlag := fs.String("cidr", huntDefaultCIDRs, "comma-separated IPv4 CIDR blocks to sweep with unicast discovery queries")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q (see: hunt -help)\n", fs.Arg(0))
		return 2
	}
	cidrs := make([]*net.IPNet, 0, 2)
	for _, spec := range strings.Split(*cidrFlag, ",") {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		_, ipn, err := net.ParseCIDR(spec)
		if err != nil || ipn.IP.To4() == nil {
			fmt.Fprintf(os.Stderr, "invalid IPv4 CIDR %q\n", spec)
			return 2
		}
		cidrs = append(cidrs, ipn)
	}
	if len(cidrs) == 0 {
		fmt.Fprintln(os.Stderr, "no valid CIDR given")
		return 2
	}

	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open a UDP socket: %v\n", err)
		return 1
	}
	defer conn.Close()
	localPort := conn.LocalAddr().(*net.UDPAddr).Port

	own := map[string]bool{}
	for _, l := range localIPv4Nets() {
		own[l.ipnet.IP.String()] = true
	}

	packets := make(chan udpPacket, 256)
	go huntReadLoop(conn, packets, localPort)

	c := newCollector()
	fmt.Printf("[hunt] sweeping %s with unicast queries (source :%d)\n", joinCIDRs(cidrs), localPort)
	seq := uint32(0)
	for round := 1; round <= huntRounds; round++ {
		sent := huntRound(conn, discoveryQueryPort, cidrs, own, packets, c, &seq)
		fmt.Printf("[hunt] round %d/%d: %d queries sent\n", round, huntRounds, sent)
		if len(c.allDiscoveries()) > 0 {
			break
		}
	}

	deadline := time.Now().Add(huntFinalWait)
	for time.Now().Before(deadline) {
		drainHuntPackets(packets, own, c)
		time.Sleep(100 * time.Millisecond)
	}

	printSummary(c)
	if len(c.allDiscoveries()) == 0 {
		fmt.Fprintln(os.Stderr, "[hunt] no lidar answered. A directly connected lidar is only reachable when the CIDR is on-link;")
		printRouteHint(os.Stderr, cidrs)
		fmt.Fprintln(os.Stderr, "[hunt] also make sure the lidar is powered on; a silent but alive lidar still leaves an ARP entry (arp -a).")
		return 1
	}
	return 0
}

// huntReadLoop forwards every datagram the hunt socket receives into packets.
func huntReadLoop(conn net.PacketConn, packets chan<- udpPacket, dstPort int) {
	buf := make([]byte, recvBufSize)
	for {
		n, addr, err := conn.ReadFrom(buf)
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
			dstPort: dstPort,
			data:    append([]byte(nil), buf[:n]...),
		}
	}
}

// huntRound sends one discovery query to every host of cidrs (skipping .0/.255
// addresses and this host's own IPs), draining replies into c along the way.
// It returns the number of queries sent. port is the lidar-side discovery
// port; tests substitute a fake lidar's port.
func huntRound(conn net.PacketConn, port int, cidrs []*net.IPNet, own map[string]bool, packets <-chan udpPacket, c *collector, seq *uint32) int {
	sent := 0
	for _, cidr := range cidrs {
		forEachHost(cidr, func(ip net.IP) {
			if own[ip.String()] {
				return
			}
			*seq++
			frame, err := livox.BuildDiscoveryQuery(*seq)
			if err != nil {
				return
			}
			if _, err := conn.WriteTo(frame, &net.UDPAddr{IP: ip, Port: port}); err == nil {
				sent++
			}
			// Pace the sweep at roughly one /24 per huntSubnetPause.
			if sent%254 == 0 {
				time.Sleep(huntSubnetPause)
			}
			drainHuntPackets(packets, own, c)
		})
	}
	return sent
}

// drainHuntPackets folds pending replies into the collector without blocking.
func drainHuntPackets(packets <-chan udpPacket, own map[string]bool, c *collector) {
	for {
		select {
		case p := <-packets:
			if !own[p.srcIP] {
				handlePacket(c, p, false)
			}
		default:
			return
		}
	}
}

// forEachHost invokes fn for every usable host address of an IPv4 CIDR:
// addresses ending in .0 or .255 are skipped, as they are network or
// broadcast addresses in every /24.
func forEachHost(cidr *net.IPNet, fn func(net.IP)) {
	ip4 := cidr.IP.To4()
	if ip4 == nil || len(cidr.Mask) != 4 {
		return
	}
	mask := binary.BigEndian.Uint32(cidr.Mask)
	base := binary.BigEndian.Uint32(ip4) & mask
	broadcast := base | ^mask
	for v := base; ; v++ {
		usable := v != base && v != broadcast && v&0xff != 0 && v&0xff != 0xff
		if usable {
			ip := make(net.IP, 4)
			binary.BigEndian.PutUint32(ip, v)
			fn(ip)
		}
		if v == broadcast || v == 0xffffffff {
			return
		}
	}
}

// printRouteHint suggests the command that makes the swept CIDRs on-link so
// the next hunt reaches a directly connected lidar.
func printRouteHint(w io.Writer, cidrs []*net.IPNet) {
	dev := "<iface>"
	if locals := localIPv4Nets(); len(locals) > 0 {
		dev = locals[0].iface
	}
	for _, cidr := range cidrs {
		ones, _ := cidr.Mask.Size()
		cidrText := fmt.Sprintf("%s/%d", cidr.IP, ones)
		if runtime.GOOS == "darwin" {
			fmt.Fprintf(w, "[hunt]   sudo route -n add -net %s -interface %s\n", cidrText, dev)
		} else {
			fmt.Fprintf(w, "[hunt]   sudo ip route add %s dev %s\n", cidrText, dev)
		}
	}
}

func joinCIDRs(cidrs []*net.IPNet) string {
	parts := make([]string, len(cidrs))
	for i, cidr := range cidrs {
		ones, _ := cidr.Mask.Size()
		parts[i] = fmt.Sprintf("%s/%d", cidr.IP, ones)
	}
	return strings.Join(parts, ",")
}
