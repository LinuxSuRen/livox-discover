// Automatic host-network provisioning: when a lidar sits in a subnet the
// host has no on-link presence in, unicast tools cannot reach it and no
// amount of broadcasting helps (see hunt). net-fix adds the missing route
// or alias through sudo — the terminal password prompt doubles as the user's
// consent — and only ever adds configuration, never changes or removes it.
//
// macOS is managed aggressively by System Settings (a mask change on a
// service-managed interface gets reverted within seconds), but additive
// routes and interface aliases survive; that asymmetry drives the design.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/livox"
)

// runPrivileged runs a system command directly when root, otherwise through
// sudo with all stdio wired to the terminal so the password prompt and any
// command output are visible.
func runPrivileged(name string, args ...string) error {
	if os.Geteuid() == 0 {
		cmd := exec.Command(name, args...)
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		return cmd.Run()
	}
	cmd := exec.Command("sudo", append([]string{name}, args...)...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	return cmd.Run()
}

// routeOnLink reports whether ip is reachable without a gateway, and through
// which interface. It uses the unprivileged route-inspection commands.
func routeOnLink(ip net.IP) (iface string, onLink bool) {
	if ip.To4() == nil {
		return "", false
	}
	var out []byte
	var err error
	if runtime.GOOS == "darwin" {
		out, err = exec.Command("route", "-n", "get", ip.String()).Output()
	} else {
		out, err = exec.Command("ip", "route", "get", ip.String()).Output()
	}
	if err != nil {
		return "", false
	}
	if runtime.GOOS == "darwin" {
		return parseRouteGetDarwin(string(out))
	}
	return parseRouteGetLinux(string(out))
}

// parseRouteGetDarwin extracts the egress interface and whether the route
// goes through a gateway from `route -n get <ip>` output.
func parseRouteGetDarwin(out string) (string, bool) {
	iface, viaGateway := "", false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "interface:"); ok {
			iface = strings.TrimSpace(v)
		}
		if strings.Contains(line, "flags:") && strings.Contains(line, "GATEWAY") {
			viaGateway = true
		}
	}
	return iface, iface != "" && !viaGateway
}

// parseRouteGetLinux does the same for `ip route get <ip>` output, where a
// "via <gw>" token indicates a gateway route.
func parseRouteGetLinux(out string) (string, bool) {
	dev, via := "", false
	fields := strings.Fields(strings.SplitN(out, "\n", 2)[0])
	for i := 0; i < len(fields)-1; i++ {
		switch fields[i] {
		case "dev":
			dev = fields[i+1]
		case "via":
			via = true
		}
	}
	return dev, dev != "" && !via
}

// addRouteArgs builds the privileged command that makes cidr on-link via dev.
func addRouteArgs(cidr, dev string) (string, []string) {
	if runtime.GOOS == "darwin" {
		return "route", []string{"-n", "add", "-net", cidr, "-interface", dev}
	}
	return "ip", []string{"route", "add", cidr, "dev", dev}
}

// addAliasArgs builds the privileged command that gives dev a secondary
// address. aliasIP carries no mask; Livox convention is /24.
func addAliasArgs(aliasIP, dev string) (string, []string) {
	if runtime.GOOS == "darwin" {
		return "ifconfig", []string{dev, "alias", aliasIP, "255.255.255.0"}
	}
	return "ip", []string{"addr", "add", aliasIP + "/24", "dev", dev}
}

// wirelessInterfaces lists Wi-Fi interface names, which must not be chosen
// as the lidar-facing device for auto fixes.
func wirelessInterfaces() map[string]bool {
	out := map[string]bool{}
	if runtime.GOOS == "darwin" {
		text, err := exec.Command("networksetup", "-listallhardwareports").Output()
		if err != nil {
			return out
		}
		// "Hardware Port: Wi-Fi" is followed by a "Device: enX" line.
		nextIsDev := false
		for _, line := range strings.Split(string(text), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Hardware Port: Wi-Fi") {
				nextIsDev = true
				continue
			}
			if nextIsDev {
				if dev, ok := strings.CutPrefix(line, "Device:"); ok {
					out[strings.TrimSpace(dev)] = true
					nextIsDev = false
				}
			}
		}
		return out
	}
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if _, err := os.Stat("/sys/class/net/" + e.Name() + "/wireless"); err == nil {
			out[e.Name()] = true
		}
	}
	return out
}

// lidarIfaceFromARP returns the interface whose ARP table holds ip — the
// wire the lidar was actually seen on.
func lidarIfaceFromARP(ip net.IP) string {
	if ip.To4() == nil {
		return ""
	}
	var out []byte
	var err error
	if runtime.GOOS == "darwin" {
		out, err = exec.Command("arp", "-n", ip.String()).Output()
	} else {
		out, err = exec.Command("ip", "neigh", "show", ip.String()).Output()
	}
	if err != nil {
		return ""
	}
	return parseIfaceFromARP(string(out))
}

// parseIfaceFromARP pulls the interface name out of `arp -n <ip>` (macOS,
// "... on en10 ifscope ...") or `ip neigh show <ip>` (Linux, "dev en10").
// Incomplete entries carry no MAC and are ignored.
func parseIfaceFromARP(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "(incomplete)") || strings.Contains(line, "FAILED") {
			continue
		}
		fields := strings.Fields(line)
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] == "on" || fields[i] == "dev" {
				return fields[i+1]
			}
		}
	}
	return ""
}

// pickWiredIface returns the single plausible lidar-facing interface: up,
// broadcast-capable, neither loopback nor Wi-Fi, and holding an IPv4 address
// (a route through an address-less interface cannot carry replies). It
// returns "" when several candidates remain — ask for -iface then.
func pickWiredIface() string {
	wifi := wirelessInterfaces()
	var candidates []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagBroadcast == 0 {
			continue
		}
		if wifi[ifc.Name] {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		hasIPv4 := false
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
				hasIPv4 = true
				break
			}
		}
		if hasIPv4 {
			candidates = append(candidates, ifc.Name)
		}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	return ""
}

// resolveIface picks the interface for an auto fix: explicit flag first,
// then the ARP entry of the lidar (when its IP is known), then the single
// wired candidate.
func resolveIface(flagIface string, lidarIP net.IP) string {
	if flagIface != "" {
		return flagIface
	}
	if lidarIP != nil {
		if dev := lidarIfaceFromARP(lidarIP); dev != "" {
			return dev
		}
	}
	return pickWiredIface()
}

// lidarAlive sends a unicast discovery query and reports whether a lidar at
// ip answers within about a second — the difference between "routing table
// says on-link" and "the lidar actually answers".
func lidarAlive(ip net.IP) bool {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return false
	}
	defer conn.Close()
	frame, err := livox.BuildDiscoveryQuery(1)
	if err != nil {
		return false
	}
	buf := make([]byte, recvBufSize)
	for i := 0; i < 2; i++ {
		conn.WriteTo(frame, &net.UDPAddr{IP: ip, Port: discoveryQueryPort})
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			continue
		}
		if _, err := livox.ParseDiscoveryACK(buf[:n]); err == nil {
			return true
		}
	}
	return false
}

// ensureLidarReachable makes the lidar reachable: it verifies the routing
// table AND that the lidar actually answers; when it does not, a /32 host
// route via dev is added (a /32 outranks any wrong longer-prefix route, so
// the fix stays additive). With alias, a /24 alias address is added instead.
func ensureLidarReachable(lidarIP net.IP, dev string, alias bool) bool {
	iface, onLink := routeOnLink(lidarIP)
	if onLink && lidarAlive(lidarIP) {
		fmt.Printf("[net-fix] %s is already reachable via %s\n", lidarIP, iface)
		return true
	}
	if onLink {
		fmt.Printf("[net-fix] %s looks on-link via %s but does not answer; adding a host route via %s\n", lidarIP, iface, dev)
	}
	if dev == "" {
		fmt.Fprintln(os.Stderr, "[net-fix] cannot tell which interface faces the lidar; pass -iface <name>")
		return false
	}
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "[net-fix] automatic network changes are not supported on Windows; configure the route manually")
		return false
	}

	var name string
	var args []string
	if alias {
		aliasIP := net.IP(append([]byte(nil), lidarIP.To4()...))
		aliasIP[3] = 50 // same convention as the printed hints
		name, args = addAliasArgs(aliasIP.String(), dev)
	} else {
		if runtime.GOOS == "darwin" {
			name = "route"
			args = []string{"-n", "add", "-host", lidarIP.String(), "-interface", dev}
		} else {
			name, args = addRouteArgs(fmt.Sprintf("%s/32", lidarIP), dev)
		}
	}
	fmt.Printf("[net-fix] running: %s %s (needs sudo)\n", name, strings.Join(args, " "))
	if err := runPrivileged(name, args...); err != nil {
		fmt.Fprintf(os.Stderr, "[net-fix] failed: %v\n", err)
		return false
	}
	if lidarAlive(lidarIP) {
		fmt.Printf("[net-fix] %s now answers via %s\n", lidarIP, dev)
		return true
	}
	fmt.Printf("[net-fix] route via %s provisioned, but %s stays silent — check it is powered on\n", dev, lidarIP)
	return true
}

// ensureCIDROnLink adds an on-link net route for cidr, used by hunt when the
// swept space never reached the wire.
func ensureCIDROnLink(cidr *net.IPNet, dev string) bool {
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "[net-fix] automatic network changes are not supported on Windows; configure the route manually")
		return false
	}
	if dev == "" {
		fmt.Fprintln(os.Stderr, "[net-fix] cannot tell which interface faces the lidar; pass -iface <name>")
		return false
	}
	ones, _ := cidr.Mask.Size()
	cidrText := fmt.Sprintf("%s/%d", cidr.IP, ones)
	name, args := addRouteArgs(cidrText, dev)
	fmt.Printf("[net-fix] running: %s %s (needs sudo)\n", name, strings.Join(args, " "))
	if err := runPrivileged(name, args...); err != nil {
		fmt.Fprintf(os.Stderr, "[net-fix] failed: %v\n", err)
		return false
	}
	fmt.Printf("[net-fix] %s is now on-link via %s\n", cidrText, dev)
	return true
}

// runNetFix implements the net-fix subcommand: make a lidar (or a whole CIDR)
// reachable from this host. Exit codes: 0 reachable (or made reachable),
// 1 could not, 2 bad usage.
func runNetFix(args []string) int {
	fs := flag.NewFlagSet("net-fix", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: %s net-fix -lidar <ip> | -cidr <cidr> [-iface enX] [-alias]\n", os.Args[0])
		fs.PrintDefaults()
	}
	lidarIP := fs.String("lidar", "", "make this lidar IP reachable (host route; -alias adds a /24 address instead)")
	cidrFlag := fs.String("cidr", "", "make this whole CIDR on-link (net route)")
	iface := fs.String("iface", "", "interface facing the lidar; auto-detected when possible")
	alias := fs.Bool("alias", false, "add a /24 alias address instead of a host route (lidar mode only)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q (see: net-fix -help)\n", fs.Arg(0))
		return 2
	}
	if (*lidarIP == "") == (*cidrFlag == "") {
		fmt.Fprintln(os.Stderr, "pass exactly one of -lidar or -cidr")
		return 2
	}
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "net-fix is not supported on Windows; configure the network manually")
		return 1
	}

	if *cidrFlag != "" {
		_, ipn, err := net.ParseCIDR(*cidrFlag)
		if err != nil || ipn.IP.To4() == nil {
			fmt.Fprintf(os.Stderr, "invalid IPv4 CIDR %q\n", *cidrFlag)
			return 2
		}
		if !ensureCIDROnLink(ipn, resolveIface(*iface, nil)) {
			return 1
		}
		return 0
	}

	ip := net.ParseIP(*lidarIP)
	if ip == nil || ip.To4() == nil {
		fmt.Fprintf(os.Stderr, "invalid IPv4 address %q\n", *lidarIP)
		return 2
	}
	if !ensureLidarReachable(ip, resolveIface(*iface, ip), *alias) {
		return 1
	}
	return 0
}
