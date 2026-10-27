package main

import (
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/hesai"
	"github.com/LinuxSuRen/livox-discover/internal/livox"
)

type hitKey struct {
	ip               string
	srcPort, dstPort int
}

type discoveryKey struct {
	sn, lidarIP string
}

// sourceInfo aggregates every datagram received from one source IP.
type sourceInfo struct {
	IP           string
	Packets      int
	LivoxPackets int         // packets whose first byte is the 0xAA start-of-frame
	HesaiPackets int         // packets recognized as Hesai MSOP point clouds
	Ports        map[int]int // local port -> packet count
	discoveries  map[discoveryKey]livox.DetectionData
}

// packetEvent reports what a single received datagram changed, so the CLI can
// log hits without the collector knowing anything about output.
type packetEvent struct {
	HitCount     int
	ShowHit      bool
	NewDiscovery *livox.DetectionData
	NewHesai     bool // first Hesai MSOP packet seen from this source IP
}

// collector reduces the packet stream to one entry per source IP. It is pure
// bookkeeping over add, which keeps it unit-testable without sockets.
type collector struct {
	sources map[string]*sourceInfo
	hits    map[hitKey]int
}

func newCollector() *collector {
	return &collector{
		sources: map[string]*sourceInfo{},
		hits:    map[hitKey]int{},
	}
}

// add records one datagram. ShowHit fires on the 1st, 50th and 500th packet
// per (source address, local port), mirroring the original tool's log cadence.
// NewDiscovery is set only the first time a given (sn, lidar_ip) shows up.
func (c *collector) add(ip string, srcPort, dstPort int, data []byte) packetEvent {
	s := c.sources[ip]
	if s == nil {
		s = &sourceInfo{
			IP:          ip,
			Ports:       map[int]int{},
			discoveries: map[discoveryKey]livox.DetectionData{},
		}
		c.sources[ip] = s
	}
	s.Packets++
	isLivox := livox.HasSOF(data)
	if isLivox {
		s.LivoxPackets++
	}
	isHesai := hesai.IsMSOP(data)
	if isHesai {
		s.HesaiPackets++
	}
	s.Ports[dstPort]++

	key := hitKey{ip: ip, srcPort: srcPort, dstPort: dstPort}
	c.hits[key]++
	count := c.hits[key]
	ev := packetEvent{
		HitCount: count,
		ShowHit:  count == 1 || count == 50 || count == 500,
		NewHesai: isHesai && s.HesaiPackets == 1,
	}

	if isLivox {
		if det, err := livox.ParseDiscoveryACK(data); err == nil {
			dkey := discoveryKey{sn: det.SN, lidarIP: det.LidarIP}
			if _, dup := s.discoveries[dkey]; !dup {
				s.discoveries[dkey] = det
				d := det
				ev.NewDiscovery = &d
			}
		}
	}
	return ev
}

// sortedSources returns the collected sources ordered by IP address
// (numerically, so 192.0.2.2 sorts before 192.0.2.10).
func (c *collector) sortedSources() []*sourceInfo {
	out := make([]*sourceInfo, 0, len(c.sources))
	for _, s := range c.sources {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return compareIP(out[i].IP, out[j].IP) < 0
	})
	return out
}

// allDiscoveries returns every decoded discovery ACK across all sources,
// regardless of ordering; used for the cross-subnet diagnostics.
func (c *collector) allDiscoveries() []livox.DetectionData {
	var out []livox.DetectionData
	for _, s := range c.sources {
		for _, d := range s.discoveries {
			out = append(out, d)
		}
	}
	return out
}

// sortedDiscoveries returns the decoded devices of one source in a stable
// (sn, lidar_ip) order.
func (s *sourceInfo) sortedDiscoveries() []livox.DetectionData {
	out := make([]livox.DetectionData, 0, len(s.discoveries))
	for _, d := range s.discoveries {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SN != out[j].SN {
			return out[i].SN < out[j].SN
		}
		return out[i].LidarIP < out[j].LidarIP
	})
	return out
}

func compareIP(a, b string) int {
	na, errA := netip.ParseAddr(a)
	nb, errB := netip.ParseAddr(b)
	if errA == nil && errB == nil {
		return na.Compare(nb)
	}
	return strings.Compare(a, b)
}

func formatPortCounts(ports map[int]int) string {
	keys := make([]int, 0, len(ports))
	for p := range ports {
		keys = append(keys, p)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, p := range keys {
		parts = append(parts, strconv.Itoa(p)+":"+strconv.Itoa(ports[p]))
	}
	return strings.Join(parts, ",")
}

// skippedPortJSON reports a port the tool could not bind.
type skippedPortJSON struct {
	Port  int    `json:"port"`
	Error string `json:"error"`
}

// sourceJSON is the per-source-IP slice of the JSON report.
type sourceJSON struct {
	IP           string                `json:"ip"`
	Packets      int                   `json:"packets"`
	LivoxPackets int                   `json:"livox_packets"`
	HesaiPackets int                   `json:"hesai_packets,omitempty"`
	Ports        map[string]int        `json:"ports"`
	Discoveries  []livox.DetectionData `json:"discoveries,omitempty"`
}

// reportJSON is the whole -json output.
type reportJSON struct {
	DurationSeconds float64           `json:"duration_seconds"`
	Interrupted     bool              `json:"interrupted,omitempty"`
	Ports           []int             `json:"ports"`
	Skipped         []skippedPortJSON `json:"skipped_ports,omitempty"`
	Sources         []sourceJSON      `json:"sources"`
}

func buildReport(c *collector, duration time.Duration, ports []int, skipped []skippedPortJSON, interrupted bool) reportJSON {
	rep := reportJSON{
		DurationSeconds: duration.Seconds(),
		Interrupted:     interrupted,
		Ports:           ports,
		Skipped:         skipped,
		Sources:         []sourceJSON{},
	}
	for _, s := range c.sortedSources() {
		sj := sourceJSON{
			IP:           s.IP,
			Packets:      s.Packets,
			LivoxPackets: s.LivoxPackets,
			HesaiPackets: s.HesaiPackets,
			Ports:        map[string]int{},
		}
		for p, n := range s.Ports {
			sj.Ports[strconv.Itoa(p)] = n
		}
		sj.Discoveries = append(sj.Discoveries, s.sortedDiscoveries()...)
		rep.Sources = append(rep.Sources, sj)
	}
	return rep
}
