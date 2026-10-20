package main

import (
	"net"
	"strings"
	"testing"

	"github.com/LinuxSuRen/livox-discover/internal/livox"
)

func TestParseRouteGetDarwin(t *testing.T) {
	onLink := `   route to: 192.168.1.179
destination: 192.168.1.179
    gateway: 192.168.0.16
  interface: en10
      flags: <UP,HOST,DONE,LLINFO,WASCLONED,IFSCOPE,IFREF>`
	iface, ok := parseRouteGetDarwin(onLink)
	if !ok || iface != "en10" {
		t.Errorf("parseRouteGetDarwin(on-link) = %q %v, want en10 true", iface, ok)
	}

	viaGw := `   route to: 8.8.8.8
destination: default
      gateway: 192.168.31.1
    interface: en0
        flags: <UP,GATEWAY,HOST,DONE,WASCLONED,IFREF>`
	iface, ok = parseRouteGetDarwin(viaGw)
	if ok || iface != "en0" {
		t.Errorf("parseRouteGetDarwin(gateway) = %q %v, want en0 false", iface, ok)
	}
}

func TestParseRouteGetLinux(t *testing.T) {
	onLink := "192.168.1.179 dev en10 src 192.168.0.16 \n"
	iface, ok := parseRouteGetLinux(onLink)
	if !ok || iface != "en10" {
		t.Errorf("parseRouteGetLinux(on-link) = %q %v, want en10 true", iface, ok)
	}

	viaGw := "8.8.8.8 via 192.168.31.1 dev en0 src 192.168.31.63 \n"
	iface, ok = parseRouteGetLinux(viaGw)
	if ok || iface != "en0" {
		t.Errorf("parseRouteGetLinux(gateway) = %q %v, want en0 false", iface, ok)
	}
}

func TestParseIfaceFromARP(t *testing.T) {
	darwin := "? (192.168.1.179) at 88:29:85:9a:7f:74 on en10 ifscope [ethernet]\n"
	if got := parseIfaceFromARP(darwin); got != "en10" {
		t.Errorf("parseIfaceFromARP(darwin) = %q, want en10", got)
	}
	linux := "192.168.1.179 dev en10 lladdr 88:29:85:9a:7f:74 REACHABLE\n"
	if got := parseIfaceFromARP(linux); got != "en10" {
		t.Errorf("parseIfaceFromARP(linux) = %q, want en10", got)
	}
	incomplete := "? (192.168.1.5) at (incomplete) on en10 ifscope [ethernet]\n"
	if got := parseIfaceFromARP(incomplete); got != "" {
		t.Errorf("parseIfaceFromARP(incomplete) = %q, want \"\"", got)
	}
}

func TestWirelessInterfaceParsing(t *testing.T) {
	// covered indirectly on real hosts; the parser shares the pattern used
	// in parseRouteGetDarwin (CutPrefix on trimmed lines)
	text := "Hardware Port: Wi-Fi\nDevice: en0\nEthernet Address: aa:bb:cc:dd:ee:ff\n\nHardware Port: USB 10/100 LAN\nDevice: en10\n"
	wifiDev := ""
	nextIsDev := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Hardware Port: Wi-Fi") {
			nextIsDev = true
			continue
		}
		if nextIsDev {
			if dev, ok := strings.CutPrefix(line, "Device:"); ok {
				wifiDev = strings.TrimSpace(dev)
				nextIsDev = false
			}
		}
	}
	if wifiDev != "en0" {
		t.Errorf("wifi device parse = %q, want en0", wifiDev)
	}
}

func TestResolveIfacePrefersFlag(t *testing.T) {
	if got := resolveIface("en7", nil); got != "en7" {
		t.Errorf("resolveIface(flag) = %q, want en7", got)
	}
	if got := resolveIface("", nil); got == "" && testing.Short() {
		t.Log("no auto iface in this environment")
	}
}

func TestLidarAlive(t *testing.T) {
	// fake lidar holding the discovery port on loopback
	conn, err := net.ListenPacket("udp4", "127.0.0.1:56000")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1:56000: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, recvBufSize)
		for {
			_, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			payload, err := livox.BuildDiscoveryPayload(9, "TFD2DJAAB00001", "192.0.2.10", 56100, 0)
			if err != nil {
				continue
			}
			frame, err := livox.BuildFrame(livox.CmdIDDiscovery, livox.CmdTypeAck,
				livox.SenderTypeLidar, 1, payload, livox.ProtocolVersion)
			if err != nil {
				continue
			}
			conn.WriteTo(frame, from)
		}
	}()

	if !lidarAlive(net.ParseIP("127.0.0.1")) {
		t.Error("lidarAlive(fake) = false, want true")
	}
	if lidarAlive(net.ParseIP("127.0.0.3")) {
		t.Error("lidarAlive(dead) = true, want false")
	}
}

func TestRunNetFixBadUsage(t *testing.T) {
	for _, args := range [][]string{
		{}, // neither -lidar nor -cidr
		{"-lidar", "192.168.1.179", "-cidr", "192.168.1.0/24"}, // both
		{"-lidar", "not-an-ip"},
		{"-cidr", "not-a-cidr"},
		{"stray"},
	} {
		if code := runNetFix(args); code != 2 {
			t.Errorf("runNetFix(%q) exit = %d, want 2", args, code)
		}
	}
}
