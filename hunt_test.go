package main

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/livox"
)

func mustParseCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, ipn, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", s, err)
	}
	return ipn
}

func TestForEachHost(t *testing.T) {
	count := func(cidr string) int {
		n := 0
		forEachHost(mustParseCIDR(t, cidr), func(net.IP) { n++ })
		return n
	}
	if got := count("192.168.0.0/24"); got != 254 {
		t.Errorf("/24 hosts = %d, want 254", got)
	}
	if got := count("192.168.0.0/30"); got != 2 {
		t.Errorf("/30 hosts = %d, want 2", got)
	}
	if got := count("192.168.0.0/16"); got != 256*254 {
		t.Errorf("/16 hosts = %d, want %d", got, 256*254)
	}
	// .0 and .255 must never appear
	forEachHost(mustParseCIDR(t, "192.168.5.0/24"), func(ip net.IP) {
		last := ip.To4()[3]
		if last == 0 || last == 255 {
			t.Errorf("host %s leaked through", ip)
		}
	})
}

func TestHuntRoundFindsFakeLidar(t *testing.T) {
	lidar := startFakeLidar(t, nil, func(req livox.CmdFrame) [][]byte {
		payload, err := livox.BuildDiscoveryPayload(35, "ARMCP150033179", "192.168.1.179", 56100, 0)
		if err != nil {
			t.Errorf("BuildDiscoveryPayload: %v", err)
		}
		ack, err := livox.BuildFrame(livox.CmdIDDiscovery, livox.CmdTypeAck,
			livox.SenderTypeLidar, req.SeqNum, payload, livox.ProtocolVersion)
		if err != nil {
			t.Errorf("BuildFrame: %v", err)
		}
		return [][]byte{ack}
	})

	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	localPort := conn.LocalAddr().(*net.UDPAddr).Port

	packets := make(chan udpPacket, 256)
	go huntReadLoop(conn, packets, localPort)

	c := newCollector()
	seq := uint32(0)
	// sweep the loopback /24 directly at the fake lidar's port; the fake's
	// 127.0.0.1 address is included because loopback is not part of own
	sent := huntRound(conn, lidar.addr().Port, []*net.IPNet{mustParseCIDR(t, "127.0.0.0/24")},
		map[string]bool{}, packets, c, &seq)
	if sent == 0 {
		t.Fatal("no queries sent")
	}

	// the reply arrives asynchronously; drain until it shows up
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(c.allDiscoveries()) == 0 {
		drainHuntPackets(packets, map[string]bool{}, c)
		time.Sleep(10 * time.Millisecond)
	}

	dets := c.allDiscoveries()
	if len(dets) != 1 {
		t.Fatalf("discoveries = %d, want 1", len(dets))
	}
	if dets[0].SN != "ARMCP150033179" || dets[0].LidarIP != "192.168.1.179" {
		t.Errorf("discovery = %+v", dets[0])
	}
}

func TestRunHuntBadUsage(t *testing.T) {
	for _, args := range [][]string{
		{"-cidr", "not-a-cidr"},
		{"-cidr", "::1/64"},
		{"-cidr", ","},
		{"stray"},
	} {
		if code := runHunt(args); code != 2 {
			t.Errorf("runHunt(%q) exit = %d, want 2", args, code)
		}
	}
}

func TestPrintRouteHint(t *testing.T) {
	var sb strings.Builder
	printRouteHint(&sb, []*net.IPNet{mustParseCIDR(t, "192.168.0.0/16")})
	out := sb.String()
	if !strings.Contains(out, "192.168.0.0/16") {
		t.Errorf("hint missing CIDR: %q", out)
	}
}
