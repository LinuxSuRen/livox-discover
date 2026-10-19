// Command livox-discover web: a small browser UI over the discovery scan,
// set-ip and reboot commands. The frontend is a Vue app embedded into the
// binary (web/dist), so a single executable serves everything offline.
//
// API shape:
//
//	POST /api/scan        {duration_seconds, probe} -> 202; 409 when running
//	GET  /api/scan        current state (same JSON the SSE stream pushes)
//	POST /api/scan/stop   stop the running scan early
//	GET  /api/events      SSE stream; event "scan" carries the state JSON
//	POST /api/set-ip      {lidar, ip, mask, gw, cmd_port}
//	POST /api/reboot      {lidar, cmd_port, delay}
//
// Errors use the envelope {error:{message, code}} with stable LVX-* codes;
// technical causes go to the server log only.
package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/livox"
)

//go:embed web/dist
var webDist embed.FS

// Stable user-facing error codes of the web API. Registered here so new
// codes never collide; the message next to a code must stay readable by a
// non-technical user and free of technical terms.
const (
	errCodeBadArgument   = "LVX-4001" // 参数不合法
	errCodeScanRunning   = "LVX-4090" // 已有扫描在运行
	errCodeSetIPNoAck    = "LVX-5001" // 雷达未应答修改请求
	errCodeRebootNoAck   = "LVX-5003" // 雷达未应答重启请求
	errCodeServerBlocked = "LVX-5005" // 后端正忙
)

const (
	webMaxScanSeconds = 300
	// webCommandTimeout bounds a set-ip/reboot HTTP request on the server
	// side in addition to the per-attempt timeouts.
	webCommandTimeout = 15 * time.Second
)

// scanStateJSON is the single state document shared by GET /api/scan and
// the SSE stream (same source, same bytes).
type scanStateJSON struct {
	Running         bool       `json:"running"`
	ID              uint64     `json:"id"`
	Probe           bool       `json:"probe"`
	DurationSeconds float64    `json:"duration_seconds"`
	ElapsedSeconds  float64    `json:"elapsed_seconds"`
	Stopped         bool       `json:"stopped"` // ended early via /api/scan/stop
	Report          reportJSON `json:"report"`
	Hints           []string   `json:"hints,omitempty"`
}

// webServer owns the single scan slot plus the SSE subscriber set.
type webServer struct {
	mu          sync.Mutex
	running     bool
	scanID      uint64
	cancel      context.CancelFunc
	cancelled   bool
	snapshot    []byte // last published state; replaced, never mutated
	subscribers map[chan []byte]struct{}
}

func newWebServer() *webServer {
	s := &webServer{subscribers: map[chan []byte]struct{}{}}
	s.snapshot = s.marshalState(&scanStateJSON{Running: false})
	return s
}

func runWeb(args []string) int {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: %s web [-listen 127.0.0.1:8080]\n", os.Args[0])
		fs.PrintDefaults()
	}
	listen := fs.String("listen", "127.0.0.1:8080", "HTTP listen address; use 0.0.0.0:8080 to open the UI to the network")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q (see: web -help)\n", fs.Arg(0))
		return 2
	}

	srv := newWebServer()
	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	fmt.Printf("[web] listening on http://%s (UI + API under /api)\n", *listen)
	if err := httpSrv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "[web] server error: %v\n", err)
		return 1
	}
	return 0
}

func (s *webServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/scan", s.handleScanStart)
	mux.HandleFunc("GET /api/scan", s.handleScanGet)
	mux.HandleFunc("POST /api/scan/stop", s.handleScanStop)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/set-ip", s.handleSetIP)
	mux.HandleFunc("POST /api/reboot", s.handleReboot)
	mux.Handle("/", spaHandler(webDist))
	return mux
}

// ---- errors and JSON helpers ----

type errorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeUserErr emits the stable envelope and logs the technical cause where
// it belongs: the server log.
func (s *webServer) writeUserErr(w http.ResponseWriter, status int, code, message, cause string) {
	if cause != "" {
		fmt.Fprintf(os.Stderr, "[web] user error %s: %s\n", code, cause)
	}
	var env errorEnvelope
	env.Error.Message = message
	env.Error.Code = code
	writeJSON(w, status, env)
}

func (s *webServer) writeBusy(w http.ResponseWriter) {
	s.writeUserErr(w, http.StatusConflict, errCodeScanRunning,
		"已有一次扫描正在进行，请等它结束后再开始新的扫描", "")
}

// ---- scan lifecycle ----

func (s *webServer) handleScanStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DurationSeconds float64 `json:"duration_seconds"`
		Probe           bool    `json:"probe"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeUserErr(w, http.StatusBadRequest, errCodeBadArgument, "请求内容无法解析，请刷新页面后重试", err.Error())
		return
	}
	if req.DurationSeconds <= 0 {
		req.DurationSeconds = defaultDuration.Seconds()
	}
	if req.DurationSeconds > webMaxScanSeconds {
		s.writeUserErr(w, http.StatusBadRequest, errCodeBadArgument,
			"扫描时长请在 "+strconv.Itoa(webMaxScanSeconds)+" 秒以内", "")
		return
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		s.writeBusy(w)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.running = true
	s.cancelled = false
	s.cancel = cancel
	s.scanID++
	id := s.scanID
	s.mu.Unlock()

	go s.runScan(ctx, time.Duration(req.DurationSeconds*float64(time.Second)), req.Probe, id)

	writeJSON(w, http.StatusAccepted, struct {
		ID              uint64  `json:"id"`
		DurationSeconds float64 `json:"duration_seconds"`
		Probe           bool    `json:"probe"`
	}{id, req.DurationSeconds, req.Probe})
}

func (s *webServer) handleScanGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	snap := s.snapshot
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write(snap)
}

func (s *webServer) handleScanStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, struct {
			Stopped bool `json:"stopped"`
		}{false})
		return
	}
	s.cancelled = true
	cancel := s.cancel
	s.mu.Unlock()
	cancel()
	writeJSON(w, http.StatusOK, struct {
		Stopped bool `json:"stopped"`
	}{true})
}

// runScan executes one scan and publishes state snapshots; it is the only
// goroutine that touches the collector, so snapshots are race-free.
func (s *webServer) runScan(ctx context.Context, duration time.Duration, probe bool, id uint64) {
	start := time.Now()
	ports, _ := parsePorts(defaultPorts)
	bound, skippedMap := openListeners(ports)
	serverLogf := func(format string, args ...any) {
		fmt.Printf("[web][scan] "+format+"\n", args...)
	}
	var skipped []skippedPortJSON
	for _, port := range ports {
		if bindErr, ok := skippedMap[port]; ok {
			serverLogf("port %d unavailable: %v", port, bindErr)
			skipped = append(skipped, skippedPortJSON{Port: port, Error: bindErr.Error()})
		}
	}

	s.publish(func() []byte {
		return s.marshalState(&scanStateJSON{
			Running: true, ID: id, Probe: probe,
			DurationSeconds: duration.Seconds(),
			Report:          emptyReport(duration, ports, skipped),
		})
	}())

	if len(bound) == 0 {
		s.finishScan(nil, id, probe, duration, skipped, ports, false, start)
		return
	}

	c, _ := listenAndCollect(ctx, bound, duration, true, serverLogf, probe, func(c *collector) {
		s.publish(s.buildState(c, true, id, probe, duration, skipped, ports, false, start))
	})
	s.mu.Lock()
	stopped := s.cancelled
	s.mu.Unlock()
	s.finishScan(c, id, probe, duration, skipped, ports, stopped, start)
}

func (s *webServer) finishScan(c *collector, id uint64, probe bool, duration time.Duration, skipped []skippedPortJSON, ports []int, stopped bool, start time.Time) {
	if c == nil {
		c = newCollector()
	}
	s.publish(s.buildState(c, false, id, probe, duration, skipped, ports, stopped, start))
	s.mu.Lock()
	s.running = false
	s.cancel = nil
	s.mu.Unlock()
}

func (s *webServer) buildState(c *collector, running bool, id uint64, probe bool, duration time.Duration, skipped []skippedPortJSON, ports []int, stopped bool, start time.Time) []byte {
	return s.marshalState(&scanStateJSON{
		Running:         running,
		ID:              id,
		Probe:           probe,
		DurationSeconds: duration.Seconds(),
		ElapsedSeconds:  time.Since(start).Seconds(),
		Stopped:         stopped,
		Report:          buildReport(c, duration, ports, skipped, false),
		Hints:           crossSubnetHints(c.allDiscoveries(), localIPv4Nets(), runtime.GOOS),
	})
}

func (s *webServer) marshalState(state *scanStateJSON) []byte {
	bs, err := json.Marshal(state)
	if err != nil {
		return []byte("{}")
	}
	return bs
}

func emptyReport(duration time.Duration, ports []int, skipped []skippedPortJSON) reportJSON {
	return buildReport(newCollector(), duration, ports, skipped, false)
}

// publish stores a snapshot if it changed and fans it out to every SSE
// subscriber (non-blocking; slow consumers catch up on the next change).
func (s *webServer) publish(bs []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bytes.Equal(s.snapshot, bs) {
		return
	}
	s.snapshot = bs
	for ch := range s.subscribers {
		select {
		case ch <- bs:
		default:
		}
	}
}

// ---- SSE ----

func (s *webServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeUserErr(w, http.StatusInternalServerError, errCodeServerBlocked, "当前环境不支持事件流，请改用页面刷新查看结果", "")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	// Push the current snapshot immediately so the first frame is never blank.
	s.mu.Lock()
	snap := s.snapshot
	s.mu.Unlock()
	writeSSEFrame(w, snap)
	flusher.Flush()

	ch := make(chan []byte, 4)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subscribers, ch)
		s.mu.Unlock()
	}()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case bs := <-ch:
			writeSSEFrame(w, bs)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n") // keep proxies from closing the stream
			flusher.Flush()
		}
	}
}

func writeSSEFrame(w http.ResponseWriter, data []byte) {
	fmt.Fprintf(w, "event: scan\ndata: %s\n\n", data)
}

// ---- set-ip and reboot ----

func (s *webServer) handleSetIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Lidar   string `json:"lidar"`
		IP      string `json:"ip"`
		Mask    string `json:"mask"`
		GW      string `json:"gw"`
		CmdPort int    `json:"cmd_port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeUserErr(w, http.StatusBadRequest, errCodeBadArgument, "请求内容无法解析，请刷新页面后重试", err.Error())
		return
	}
	if req.Mask == "" {
		req.Mask = "255.255.255.0"
	}
	if req.CmdPort == 0 {
		req.CmdPort = defaultLidarCmdPort
	}
	if req.Lidar == "" || req.IP == "" {
		s.writeUserErr(w, http.StatusBadRequest, errCodeBadArgument, "请填写雷达当前 IP 和新 IP", "")
		return
	}
	gateway := req.GW
	if gateway == "" {
		var err error
		if gateway, err = deriveGateway(req.IP); err != nil {
			s.writeUserErr(w, http.StatusBadRequest, errCodeBadArgument, "新 IP 格式不正确", err.Error())
			return
		}
	}
	cfg := livox.LidarIPConfig{IP: req.IP, Mask: req.Mask, Gateway: gateway}
	if _, err := livox.BuildSetLidarIPPayload(cfg); err != nil {
		s.writeUserErr(w, http.StatusBadRequest, errCodeBadArgument, "IP、子网掩码或网关的格式不正确", err.Error())
		return
	}
	targetIP := net.ParseIP(req.Lidar)
	if targetIP == nil || targetIP.To4() == nil {
		s.writeUserErr(w, http.StatusBadRequest, errCodeBadArgument, "雷达当前 IP 格式不正确", req.Lidar)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), webCommandTimeout)
	defer cancel()
	outcome := performSetIPOnCtx(ctx, targetIP, req.CmdPort, cfg)
	if outcome.err != nil {
		s.writeUserErr(w, http.StatusGatewayTimeout, errCodeSetIPNoAck,
			"雷达没有应答，请确认填的是雷达当前 IP，且雷达与本机在同一网段或已添加路由", outcome.err.Error())
		return
	}
	ack := *outcome.ack
	writeJSON(w, http.StatusOK, struct {
		OK       bool   `json:"ok"`
		RetCode  byte   `json:"ret_code"`
		ErrorKey uint16 `json:"error_key"`
		Lidar    string `json:"lidar"`
		IP       string `json:"ip"`
		Mask     string `json:"mask"`
		Gateway  string `json:"gw"`
		Next     string `json:"next,omitempty"`
	}{
		OK: ack.OK(), RetCode: ack.RetCode, ErrorKey: ack.ErrorKey,
		Lidar: req.Lidar, IP: cfg.IP, Mask: cfg.Mask, Gateway: cfg.Gateway,
		Next: "配置已保存，重启雷达后生效",
	})
}

func (s *webServer) handleReboot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Lidar   string `json:"lidar"`
		CmdPort int    `json:"cmd_port"`
		Delay   uint32 `json:"delay"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeUserErr(w, http.StatusBadRequest, errCodeBadArgument, "请求内容无法解析，请刷新页面后重试", err.Error())
		return
	}
	if req.CmdPort == 0 {
		req.CmdPort = defaultLidarCmdPort
	}
	if req.Delay == 0 {
		req.Delay = livox.DefaultRebootDelay
	}
	if req.Lidar == "" {
		s.writeUserErr(w, http.StatusBadRequest, errCodeBadArgument, "请填写雷达当前 IP", "")
		return
	}
	targetIP := net.ParseIP(req.Lidar)
	if targetIP == nil || targetIP.To4() == nil {
		s.writeUserErr(w, http.StatusBadRequest, errCodeBadArgument, "雷达当前 IP 格式不正确", req.Lidar)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), webCommandTimeout)
	defer cancel()
	outcome := performRebootOnCtx(ctx, targetIP, req.CmdPort, req.Delay)
	if outcome.err != nil {
		s.writeUserErr(w, http.StatusGatewayTimeout, errCodeRebootNoAck,
			"雷达没有应答，请确认填的是雷达当前 IP，且雷达与本机在同一网段或已添加路由", outcome.err.Error())
		return
	}
	ack := *outcome.ack
	writeJSON(w, http.StatusOK, struct {
		OK      bool   `json:"ok"`
		RetCode byte   `json:"ret_code"`
		Next    string `json:"next,omitempty"`
	}{ack.OK(), ack.RetCode, "雷达正在重启，通常一分钟内完成"})
}

// performSetIPOnCtx wraps performSetIP with a hard deadline so an HTTP
// request can never hang past webCommandTimeout.
func performSetIPOnCtx(ctx context.Context, lidarIP net.IP, cmdPort int, cfg livox.LidarIPConfig) setIPOutcome {
	done := make(chan setIPOutcome, 1)
	go func() { done <- performSetIP(lidarIP, cmdPort, cfg, nil) }()
	select {
	case out := <-done:
		return out
	case <-ctx.Done():
		return setIPOutcome{err: ctx.Err()}
	}
}

func performRebootOnCtx(ctx context.Context, lidarIP net.IP, cmdPort int, delay uint32) rebootOutcome {
	done := make(chan rebootOutcome, 1)
	go func() { done <- performReboot(lidarIP, cmdPort, delay, nil) }()
	select {
	case out := <-done:
		return out
	case <-ctx.Done():
		return rebootOutcome{err: ctx.Err()}
	}
}

// ---- static assets ----

// spaHandler serves the embedded frontend, falling back to index.html for
// unknown paths.
func spaHandler(fsys embed.FS) http.Handler {
	dist, err := fs.Sub(fsys, "web/dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		if _, err := fs.Stat(dist, strings.TrimPrefix(path, "/")); err != nil {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
