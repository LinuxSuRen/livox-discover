// Active discovery probing and cross-subnet diagnostics.
//
// Real lidars (verified on a Mid360S) answer discovery queries only when
// they are sent FROM source port 56000 — exactly how the official SDK
// emits them — and may stay completely silent otherwise. -probe therefore
// reuses the tool's 56000 listener as the sender and broadcasts one query
// per second to every local subnet's directed broadcast address. When a
// discovered lidar sits outside every local subnet, the summary prints
// concrete fix commands, because unicast tools (drivers, set-ip) cannot
// reach such a lidar without an added address or route.
package main

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/livox"
)

// discoveryQueryPort is the port lidars listen on for discovery queries and
// the source port queries must originate from (kDetectionPort).
const discoveryQueryPort = 56000

// probeInterval matches the SDK's one-query-per-second detection cadence.
const probeInterval = time.Second

// startProbes broadcasts a discovery query every second from queryConn —
// which must be the 56000 listener, since lidars only answer queries with
// that source port — to every local subnet's directed broadcast address.
// It returns a stop function plus this host's own IPv4 addresses so the
// loopback copies of the broadcasts are not mistaken for lidar traffic.
func startProbes(queryConn net.PacketConn, logf func(string, ...any)) (func(), map[string]bool) {
	own := map[string]bool{}
	if queryConn == nil {
		logf("[probe] the 56000 listener is not bound; probing needs it as its query source, probe disabled")
		return func() {}, own
	}
	locals := localIPv4Nets()
	if len(locals) == 0 {
		logf("[probe] no IPv4 subnet found; probe disabled")
		return func() {}, own
	}

	var targets []*net.UDPAddr
	var desc []string
	for _, l := range locals {
		targets = append(targets, &net.UDPAddr{IP: broadcastIP(l.ipnet), Port: discoveryQueryPort})
		ones, _ := l.ipnet.Mask.Size()
		desc = append(desc, fmt.Sprintf("%s/%d via %s", l.ipnet.IP, ones, l.iface))
		own[l.ipnet.IP.String()] = true
	}
	logf("[probe] broadcasting discovery queries to %s", strings.Join(desc, ", "))

	done := make(chan struct{})
	go func() {
		seq := uint32(0)
		sendAll := func() {
			seq++
			frame, err := livox.BuildDiscoveryQuery(seq)
			if err != nil {
				return
			}
			for _, t := range targets {
				queryConn.WriteTo(frame, t)
			}
		}
		sendAll() // first probe right away
		ticker := time.NewTicker(probeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				sendAll()
			}
		}
	}()

	return func() { close(done) }, own
}

// broadcastIP returns the network broadcast address of an IPv4 subnet.
func broadcastIP(n *net.IPNet) net.IP {
	out := make(net.IP, len(n.IP))
	for i := range n.IP {
		out[i] = n.IP[i] | ^n.Mask[i]
	}
	return out.To4()
}

// localNet is one IPv4 address of this host with the interface it is on.
type localNet struct {
	ipnet *net.IPNet
	iface string
}

// localIPv4Nets lists the host's IPv4 subnets, excluding loopback and
// link-local addresses.
func localIPv4Nets() []localNet {
	var out []localNet
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			v4 := ipn.IP.To4()
			if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, localNet{
				ipnet: &net.IPNet{IP: v4, Mask: ipn.Mask},
				iface: ifc.Name,
			})
		}
	}
	return out
}

// crossSubnetHints returns diagnostic lines when any discovered lidar sits
// outside every local subnet: broadcast discovery can still see such a
// lidar, but unicast tools (drivers, set-ip) cannot reach it without an
// added address or route. goos picks the suggested command syntax.
func crossSubnetHints(dets []livox.DetectionData, locals []localNet, goos string) []string {
	var ips []string
	for _, d := range dets {
		ips = append(ips, d.LidarIP)
	}
	return crossSubnetHintLines(ips, locals, goos)
}

// crossSubnetHintLines is crossSubnetHints over bare lidar IPs, so sources
// that were recognized without a discovery ACK — a Hesai lidar seen through
// its MSOP broadcasts, say — get the same advice.
func crossSubnetHintLines(ips []string, locals []localNet, goos string) []string {
	if len(locals) == 0 || len(ips) == 0 {
		return nil
	}

	var localDesc []string
	for _, l := range locals {
		ones, _ := l.ipnet.Mask.Size()
		localDesc = append(localDesc, fmt.Sprintf("%s/%d on %s", l.ipnet.IP, ones, l.iface))
	}

	// Group offending lidar IPs by their presumed /24 (Livox ships /24).
	type group struct{ ips []string }
	groups := map[string]*group{}
	var order []string
	for _, lidarIP := range ips {
		ip := net.ParseIP(lidarIP)
		if ip == nil || ip.To4() == nil {
			continue
		}
		inside := false
		for _, l := range locals {
			if l.ipnet.Contains(ip) {
				inside = true
				break
			}
		}
		if inside {
			continue
		}
		v4 := ip.To4()
		key := fmt.Sprintf("%d.%d.%d", v4[0], v4[1], v4[2])
		g := groups[key]
		if g == nil {
			g = &group{}
			groups[key] = g
			order = append(order, key)
		}
		g.ips = append(g.ips, lidarIP)
	}
	if len(groups) == 0 {
		return nil
	}

	dev := locals[0].iface
	var out []string
	for _, key := range order {
		g := groups[key]
		out = append(out, fmt.Sprintf("[hint] lidar %s is outside every local subnet (local: %s)",
			strings.Join(g.ips, ", "), strings.Join(localDesc, ", ")))
		out = append(out, "[hint] broadcast discovery still sees it, but unicast tools (drivers, set-ip) cannot reach it")
		if goos == "darwin" {
			out = append(out, fmt.Sprintf("[hint] to reach it:     sudo ifconfig %s alias %s.50 255.255.255.0", dev, key))
			out = append(out, fmt.Sprintf("[hint] or route only:  sudo route -n add -host %s -interface %s", g.ips[0], dev))
		} else {
			out = append(out, fmt.Sprintf("[hint] to reach it:     sudo ip addr add %s.50/24 dev %s", key, dev))
			out = append(out, fmt.Sprintf("[hint] or route only:  sudo ip route add %s/32 dev %s", g.ips[0], dev))
		}
	}
	return out
}
