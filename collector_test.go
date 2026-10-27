package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/livox"
)

// mustAck builds a full discovery ACK datagram for tests.
func mustAck(t *testing.T, devType byte, sn, ip string, cmdPort uint16) []byte {
	t.Helper()
	payload, err := livox.BuildDiscoveryPayload(devType, sn, ip, cmdPort, 0)
	if err != nil {
		t.Fatalf("BuildDiscoveryPayload: %v", err)
	}
	frame, err := livox.BuildFrame(livox.CmdIDDiscovery, livox.CmdTypeAck, livox.SenderTypeLidar, 1, payload, livox.ProtocolVersion)
	if err != nil {
		t.Fatalf("BuildFrame: %v", err)
	}
	return frame
}

func TestCollectorCounts(t *testing.T) {
	c := newCollector()
	ack := mustAck(t, 9, "TFD2DJAAB00001", "192.0.2.10", 56100)
	garbage := []byte("not a livox packet at all")

	for i := 0; i < 3; i++ {
		c.add("192.0.2.10", 56101, 56000, ack)
	}
	c.add("192.0.2.10", 56101, 56201, garbage)
	c.add("192.0.2.11", 52000, 55000, garbage)

	sources := c.sortedSources()
	if len(sources) != 2 {
		t.Fatalf("got %d sources, want 2", len(sources))
	}
	a, b := sources[0], sources[1]
	if a.IP != "192.0.2.10" || b.IP != "192.0.2.11" {
		t.Errorf("source IPs = {%s, %s}, want {192.0.2.10, 192.0.2.11}", a.IP, b.IP)
	}
	if a.Packets != 4 || a.LivoxPackets != 3 {
		t.Errorf("192.0.2.10 packets=%d livox=%d, want 4/3", a.Packets, a.LivoxPackets)
	}
	if got := a.Ports[56000]; got != 3 {
		t.Errorf("a.Ports[56000] = %d, want 3", got)
	}
	if got := a.Ports[56201]; got != 1 {
		t.Errorf("a.Ports[56201] = %d, want 1", got)
	}
	if b.Packets != 1 || b.LivoxPackets != 0 {
		t.Errorf("192.0.2.11 packets=%d livox=%d, want 1/0", b.Packets, b.LivoxPackets)
	}
}

func TestCollectorSortsIPsNumerically(t *testing.T) {
	c := newCollector()
	for _, ip := range []string{"192.0.2.10", "192.0.2.2", "192.0.2.9"} {
		c.add(ip, 1000, 56000, []byte{0x00})
	}
	got := c.sortedSources()
	want := []string{"192.0.2.2", "192.0.2.9", "192.0.2.10"}
	for i := range want {
		if got[i].IP != want[i] {
			t.Fatalf("order = %v, %v, %v; want %v", got[0].IP, got[1].IP, got[2].IP, want)
		}
	}
}

func TestCollectorHitCadence(t *testing.T) {
	tests := []struct {
		count int
		show  bool
	}{
		{1, true},
		{2, false},
		{49, false},
		{50, true},
		{51, false},
		{499, false},
		{500, true},
		{501, false},
	}
	c := newCollector()
	var ev packetEvent
	for i, tt := range tests {
		// A fresh collector per case would hide off-by-ones; feed them all
		// through one key so counts grow monotonically.
		for n := (func() int {
			if i == 0 {
				return 1
			}
			return tests[i].count - tests[i-1].count
		})(); n > 0; n-- {
			ev = c.add("192.0.2.10", 56101, 56000, []byte{0x00})
		}
		if ev.HitCount != tt.count || ev.ShowHit != tt.show {
			t.Errorf("after %d packets: HitCount=%d ShowHit=%v, want %d/%v",
				tt.count, ev.HitCount, ev.ShowHit, tt.count, tt.show)
		}
	}
}

func TestCollectorHitKeyIncludesPorts(t *testing.T) {
	c := newCollector()
	first := c.add("192.0.2.10", 56101, 56000, nil)
	otherPort := c.add("192.0.2.10", 56101, 56201, nil)
	otherSrc := c.add("192.0.2.10", 56102, 56000, nil)
	if !first.ShowHit || !otherPort.ShowHit || !otherSrc.ShowHit {
		t.Errorf("hit flags = %v/%v/%v, want all true (distinct keys)", first.ShowHit, otherPort.ShowHit, otherSrc.ShowHit)
	}
}

func TestCollectorDiscoveryDedupe(t *testing.T) {
	c := newCollector()
	ack := mustAck(t, 9, "TFD2DJAAB00001", "192.0.2.10", 56100)
	otherSN := mustAck(t, 35, "TFD2DJAAB00002", "192.0.2.10", 56100)
	sameSNNewPort := mustAck(t, 9, "TFD2DJAAB00001", "192.0.2.10", 56099)

	if ev := c.add("192.0.2.10", 56101, 56000, ack); ev.NewDiscovery == nil {
		t.Fatal("first ACK should be reported as a new discovery")
	}
	for i, p := range [][]byte{ack, otherSN, sameSNNewPort} {
		ev := c.add("192.0.2.10", 56101, 56000, p)
		switch i {
		case 0:
			if ev.NewDiscovery != nil {
				t.Error("duplicate (sn, ip) should not be reported again")
			}
		case 1:
			if ev.NewDiscovery == nil || ev.NewDiscovery.SN != "TFD2DJAAB00002" {
				t.Error("new SN should be reported as a new discovery")
			}
		case 2:
			if ev.NewDiscovery != nil {
				t.Error("same SN with a changed cmd_port should not be reported again")
			}
		}
	}
	s := c.sortedSources()[0]
	dets := s.sortedDiscoveries()
	if len(dets) != 2 {
		t.Fatalf("got %d discoveries, want 2", len(dets))
	}
	if dets[0].SN != "TFD2DJAAB00001" || dets[1].SN != "TFD2DJAAB00002" {
		t.Errorf("discovery order = {%s, %s}, want sorted by SN", dets[0].SN, dets[1].SN)
	}
}

func TestCollectorToleratesMalformedLivox(t *testing.T) {
	c := newCollector()
	// 0xAA-headed but too short to be a frame: counted as livox, no discovery.
	truncated := append([]byte{0xAA}, make([]byte, 10)...)
	// 0xAA-headed, full header, but corrupted CRC: still no panic, no discovery.
	ack := mustAck(t, 9, "SN", "192.0.2.3", 56100)
	ack[5] ^= 0xFF

	for i := 0; i < 3; i++ {
		if ev := c.add("192.0.2.3", 1, 56000, truncated); ev.NewDiscovery != nil {
			t.Fatal("truncated frame must not decode as a discovery")
		}
		if ev := c.add("192.0.2.3", 1, 56000, ack); ev.NewDiscovery != nil {
			t.Fatal("corrupted frame must not decode as a discovery")
		}
	}
	s := c.sortedSources()[0]
	if s.Packets != 6 || s.LivoxPackets != 6 || len(s.sortedDiscoveries()) != 0 {
		t.Errorf("packets=%d livox=%d discoveries=%d, want 6/6/0", s.Packets, s.LivoxPackets, len(s.sortedDiscoveries()))
	}
}

func TestCollectorCountsHesaiMSOP(t *testing.T) {
	c := newCollector()
	// A JT128-shaped MSOP packet: 0xEE 0xFF marker, 1100 bytes total.
	msop := append([]byte{0xEE, 0xFF, 0x01, 0x04}, make([]byte, 1096)...)
	// Velodyne data packets share port 2368 but start 0xFF 0xEE.
	velodyne := append([]byte{0xFF, 0xEE}, make([]byte, 1098)...)

	first := c.add("192.0.2.20", 10000, 2368, msop)
	if !first.NewHesai {
		t.Error("the first MSOP packet should be reported as a new Hesai source")
	}
	if second := c.add("192.0.2.20", 10000, 2368, msop); second.NewHesai {
		t.Error("the second MSOP packet must not re-report the source")
	}
	c.add("192.0.2.21", 10000, 2368, velodyne)

	sources := c.sortedSources()
	if len(sources) != 2 {
		t.Fatalf("got %d sources, want 2", len(sources))
	}
	hesaiSrc, other := sources[0], sources[1]
	if hesaiSrc.IP != "192.0.2.20" || hesaiSrc.HesaiPackets != 2 || hesaiSrc.LivoxPackets != 0 {
		t.Errorf("192.0.2.20 hesai=%d livox=%d, want 2/0", hesaiSrc.HesaiPackets, hesaiSrc.LivoxPackets)
	}
	if other.IP != "192.0.2.21" || other.HesaiPackets != 0 {
		t.Errorf("velodyne-order packet counted as hesai: %+v", other)
	}

	rep := buildReport(c, time.Second, []int{2368}, nil, false)
	if rep.Sources[0].HesaiPackets != 2 {
		t.Errorf("report hesai_packets = %d, want 2", rep.Sources[0].HesaiPackets)
	}
	if rep.Sources[1].HesaiPackets != 0 {
		t.Errorf("report hesai_packets for the velodyne source = %d, want 0", rep.Sources[1].HesaiPackets)
	}
}

func TestLidarSidePortWarning(t *testing.T) {
	tests := []struct {
		name  string
		ports []int
		want  string
	}{
		{"defaults stay silent", []int{55000, 56000, 56201}, ""},
		{"host-side ports stay silent", []int{56101, 56301}, ""},
		{"single lidar-side port", []int{56000, 56100},
			"[warn] 56100 is a lidar-side port: the lidar listens there but never sends; " +
				"to observe its traffic listen on 56101 instead (discovery broadcasts arrive on 56000)"},
		{"all lidar-side ports", []int{56100, 56200, 56300, 56400, 56500},
			"[warn] 56100,56200,56300,56400,56500 are lidar-side ports: the lidar listens there but never sends; " +
				"to observe its traffic listen on 56101,56201,56301,56401,56501 instead (discovery broadcasts arrive on 56000)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lidarSidePortWarning(tt.ports); got != tt.want {
				t.Errorf("lidarSidePortWarning(%v) =\n %q\nwant\n %q", tt.ports, got, tt.want)
			}
		})
	}
}

func TestParsePorts(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    []int
		wantErr string
	}{
		{"defaults", "55000,56000,56201", []int{55000, 56000, 56201}, ""},
		{"spaces are trimmed", " 55000 , 56201 ", []int{55000, 56201}, ""},
		{"single port", "56100", []int{56100}, ""},
		{"duplicates collapse", "56000,56000,56201", []int{56000, 56201}, ""},
		{"trailing comma", "56000,", []int{56000}, ""},
		{"not a number", "http", nil, "invalid port"},
		{"out of range", "70000", nil, "invalid port"},
		{"zero", "0", nil, "invalid port"},
		{"negative", "-1", nil, "invalid port"},
		{"empty spec", "", nil, "no valid ports"},
		{"only commas", ",,", nil, "no valid ports"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePorts(tt.spec)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePorts(%q) error = %v", tt.spec, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ports = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("ports = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestParseVendors(t *testing.T) {
	got, err := parseVendors("livox")
	if err != nil || !got["livox"] || got["hesai"] {
		t.Errorf("parseVendors(livox) = %v, %v", got, err)
	}
	got, err = parseVendors(" Livox , hesai ")
	if err != nil || !got["livox"] || !got["hesai"] {
		t.Errorf("parseVendors mixed = %v, %v", got, err)
	}
	if _, err := parseVendors("livox,velodyne"); err == nil {
		t.Error("parseVendors should reject an unknown family")
	}
	if _, err := parseVendors(","); err == nil {
		t.Error("parseVendors should reject an empty list")
	}
}

func TestHexHead(t *testing.T) {
	data := []byte{0xAA, 0x00, 0x30, 0xFF}
	if got := hexHead(data, 16); got != "aa 00 30 ff" {
		t.Errorf("hexHead = %q, want %q", got, "aa 00 30 ff")
	}
	if got := hexHead(data, 2); got != "aa 00" {
		t.Errorf("hexHead(2) = %q, want %q", got, "aa 00")
	}
	if got := hexHead(nil, 16); got != "" {
		t.Errorf("hexHead(nil) = %q, want empty", got)
	}
}

func TestBuildReport(t *testing.T) {
	c := newCollector()
	c.add("192.0.2.10", 56101, 56000, mustAck(t, 9, "TFD2DJAAB00001", "192.0.2.10", 56100))
	c.add("192.0.2.10", 56101, 56000, mustAck(t, 9, "TFD2DJAAB00001", "192.0.2.10", 56100))

	rep := buildReport(c, 15*time.Second, []int{55000, 56000, 56201},
		[]skippedPortJSON{{Port: 55000, Error: "busy"}}, false)
	if rep.DurationSeconds != 15 {
		t.Errorf("duration = %v, want 15", rep.DurationSeconds)
	}
	if rep.Interrupted {
		t.Error("Interrupted = true, want false")
	}
	if len(rep.Sources) != 1 || rep.Sources[0].IP != "192.0.2.10" {
		t.Fatalf("sources = %+v", rep.Sources)
	}
	src := rep.Sources[0]
	if src.Packets != 2 || src.LivoxPackets != 2 {
		t.Errorf("packets = %d/%d, want 2/2", src.Packets, src.LivoxPackets)
	}
	if src.Ports["56000"] != 2 {
		t.Errorf("ports = %v, want {56000:2}", src.Ports)
	}
	if len(src.Discoveries) != 1 || src.Discoveries[0].SN != "TFD2DJAAB00001" {
		t.Errorf("discoveries = %+v", src.Discoveries)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0].Port != 55000 {
		t.Errorf("skipped = %+v", rep.Skipped)
	}

	// The report must serialize into the documented shape.
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"duration_seconds", "ports", "skipped_ports", "sources"} {
		if _, ok := m[key]; !ok {
			t.Errorf("report json missing key %q: %s", key, b)
		}
	}
	sm := m["sources"].([]any)[0].(map[string]any)
	for _, key := range []string{"ip", "packets", "livox_packets", "ports", "discoveries"} {
		if _, ok := sm[key]; !ok {
			t.Errorf("source json missing key %q: %s", key, b)
		}
	}
}
