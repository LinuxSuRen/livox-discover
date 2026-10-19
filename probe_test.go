package main

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/LinuxSuRen/livox-discover/internal/livox"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, ipn, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", s, err)
	}
	return ipn
}

func TestCrossSubnetHints(t *testing.T) {
	ipn := mustCIDR(t, "192.168.31.5/24")
	locals := []localNet{
		{ipnet: &net.IPNet{IP: net.ParseIP("192.168.31.5").To4(), Mask: ipn.Mask}, iface: "en0"},
	}
	dets := []livox.DetectionData{
		{LidarIP: "192.168.31.99"}, // inside en0
		{LidarIP: "192.168.1.100"}, // classic factory default, outside
		{LidarIP: "192.168.1.101"}, // same foreign subnet, grouped
	}

	if got := crossSubnetHints(dets[:1], locals, "linux"); got != nil {
		t.Errorf("inside lidar produced hints: %v", got)
	}
	if got := crossSubnetHints(nil, locals, "linux"); got != nil {
		t.Errorf("no detections produced hints: %v", got)
	}
	if got := crossSubnetHints(dets, nil, "linux"); got != nil {
		t.Errorf("no locals produced hints: %v", got)
	}

	joined := strings.Join(crossSubnetHints(dets, locals, "linux"), "\n")
	for _, want := range []string{
		"192.168.1.100, 192.168.1.101",
		"192.168.31.5/24 on en0",
		"sudo ip addr add 192.168.1.50/24 dev en0",
		"sudo ip route add 192.168.1.100/32 dev en0",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("linux hints missing %q in:\n%s", want, joined)
		}
	}

	joined = strings.Join(crossSubnetHints(dets, locals, "darwin"), "\n")
	for _, want := range []string{
		"sudo ifconfig en0 alias 192.168.1.50 255.255.255.0",
		"sudo route -n add -host 192.168.1.100 -interface en0",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("darwin hints missing %q in:\n%s", want, joined)
		}
	}
}

func TestBroadcastIP(t *testing.T) {
	tests := []struct {
		cidr, want string
	}{
		{"192.168.31.5/24", "192.168.31.255"},
		{"10.42.0.233/24", "10.42.0.255"},
		{"192.168.144.6/24", "192.168.144.255"},
	}
	for _, tt := range tests {
		ipn := mustCIDR(t, tt.cidr)
		if got := broadcastIP(&net.IPNet{IP: ipn.IP.To4(), Mask: ipn.Mask}); got.String() != tt.want {
			t.Errorf("broadcastIP(%q) = %s, want %s", tt.cidr, got, tt.want)
		}
	}
}

func TestLocalIPv4Nets(t *testing.T) {
	for _, l := range localIPv4Nets() {
		if l.ipnet.IP.To4() == nil {
			t.Errorf("non-IPv4 address %q listed", l.ipnet.IP)
		}
		if l.ipnet.IP.IsLoopback() {
			t.Errorf("loopback address %q listed", l.ipnet.IP)
		}
		if l.iface == "" {
			t.Errorf("address %q has no interface name", l.ipnet.IP)
		}
	}
	// The exact set is host-dependent; only sanity is asserted. A host with
	// just loopback may legitimately produce an empty list.
}

func TestStartProbesNeedsQueryPort(t *testing.T) {
	stop, own := startProbes(nil, func(string, ...any) {})
	defer stop()
	if len(own) != 0 {
		t.Errorf("own ips = %v without a query socket, want empty", own)
	}
}

func TestRunRebootAgainstFakeLidar(t *testing.T) {
	lidar := startFakeLidar(t, nil, func(req livox.CmdFrame) [][]byte {
		if req.CmdID != livox.CmdIDRebootDevice {
			t.Errorf("cmd_id = %#04x, want reboot", req.CmdID)
		}
		ack, err := livox.BuildFrame(livox.CmdIDRebootDevice, livox.CmdTypeAck,
			livox.SenderTypeLidar, req.SeqNum, []byte{0}, livox.ProtocolVersion)
		if err != nil {
			t.Errorf("BuildFrame: %v", err)
		}
		return [][]byte{ack}
	})
	code := runReboot([]string{"-lidar", "127.0.0.1", "-cmd-port", portOf(lidar)})
	if code != 0 {
		t.Errorf("runReboot exit = %d, want 0", code)
	}
}

func TestRunRebootBadUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"-lidar", "not-an-ip"},
		{"-lidar", "192.168.1.100", "-cmd-port", "70000"},
		{"stray", "-lidar", "192.168.1.100"},
	} {
		if code := runReboot(args); code != 2 {
			t.Errorf("runReboot(%q) exit = %d, want 2", args, code)
		}
	}
}

func portOf(f *fakeLidar) string {
	return strconv.Itoa(f.addr().Port)
}
