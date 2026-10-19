package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LinuxSuRen/livox-discover/internal/livox"
)

func newTestWebServer(t *testing.T) (*webServer, *httptest.Server) {
	t.Helper()
	s := newWebServer()
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	return s, ts
}

func postJSON(t *testing.T, url string, body any) (*http.Response, map[string]any) {
	t.Helper()
	bs, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", strings.NewReader(string(bs)))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return resp, out
}

func waitScanDone(t *testing.T, ts *httptest.Server) scanStateJSON {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/scan")
		if err != nil {
			t.Fatalf("GET /api/scan: %v", err)
		}
		var state scanStateJSON
		json.NewDecoder(resp.Body).Decode(&state)
		resp.Body.Close()
		if !state.Running {
			return state
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("scan did not finish in time")
	return scanStateJSON{}
}

func TestWebScanLifecycle(t *testing.T) {
	_, ts := newTestWebServer(t)

	resp, body := postJSON(t, ts.URL+"/api/scan", map[string]any{"duration_seconds": 3, "probe": false})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("scan start = %d, want 202 (body %v)", resp.StatusCode, body)
	}

	// a second scan must be rejected while the first one runs
	resp2, body2 := postJSON(t, ts.URL+"/api/scan", map[string]any{"duration_seconds": 1})
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("second scan = %d, want 409 (body %v)", resp2.StatusCode, body2)
	}
	if code := body2["error"].(map[string]any)["code"]; code != errCodeScanRunning {
		t.Errorf("second scan code = %v, want %s", code, errCodeScanRunning)
	}

	// stop shortens the wait and is reported in the final state
	resp3, body3 := postJSON(t, ts.URL+"/api/scan/stop", nil)
	if resp3.StatusCode != http.StatusOK || body3["stopped"] != true {
		t.Fatalf("scan stop = %d %v, want 200 stopped=true", resp3.StatusCode, body3)
	}

	state := waitScanDone(t, ts)
	if state.Running {
		t.Fatal("state still running after completion")
	}
	if !state.Stopped {
		t.Error("final state should report stopped=true")
	}
	if state.Report.DurationSeconds != 3 {
		t.Errorf("duration = %v, want 3", state.Report.DurationSeconds)
	}

	// stop when idle is a harmless no-op
	resp4, _ := postJSON(t, ts.URL+"/api/scan/stop", nil)
	if resp4.StatusCode != http.StatusOK {
		t.Errorf("idle stop = %d, want 200", resp4.StatusCode)
	}
}

func TestWebScanRejectsHugeDuration(t *testing.T) {
	_, ts := newTestWebServer(t)
	resp, body := postJSON(t, ts.URL+"/api/scan", map[string]any{"duration_seconds": 9999})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("huge duration = %d, want 400", resp.StatusCode)
	}
	if body["error"].(map[string]any)["code"] != errCodeBadArgument {
		t.Errorf("code = %v, want %s", body["error"], errCodeBadArgument)
	}
}

func TestWebSSEInitialFrame(t *testing.T) {
	_, ts := newTestWebServer(t)
	resp, err := ts.Client().Get(ts.URL + "/api/events")
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Error("missing X-Accel-Buffering: no header")
	}

	// the current snapshot must arrive as the very first frame
	buf := make([]byte, 512)
	n, err := resp.Body.Read(buf)
	if err != nil && n == 0 {
		t.Fatalf("no initial SSE frame: %v", err)
	}
	frame := string(buf[:n])
	if !strings.Contains(frame, "event: scan") || !strings.Contains(frame, `"running":false`) {
		t.Errorf("initial frame = %q, want event: scan with state JSON", frame)
	}
}

func TestWebSetIPAgainstFakeLidar(t *testing.T) {
	lidar := startFakeLidar(t, nil, func(req livox.CmdFrame) [][]byte {
		ack, err := livox.BuildFrame(livox.CmdIDWorkModeControl, livox.CmdTypeAck,
			livox.SenderTypeLidar, req.SeqNum, []byte{0, 0, 0}, livox.ProtocolVersion)
		if err != nil {
			t.Errorf("BuildFrame: %v", err)
		}
		return [][]byte{ack}
	})
	_, ts := newTestWebServer(t)

	resp, body := postJSON(t, ts.URL+"/api/set-ip", map[string]any{
		"lidar":    "127.0.0.1",
		"ip":       "192.168.1.120",
		"cmd_port": lidar.addr().Port,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set-ip = %d (body %v)", resp.StatusCode, body)
	}
	if body["ok"] != true {
		t.Errorf("ok = %v, want true (body %v)", body["ok"], body)
	}
	if body["next"] != "配置已保存，重启雷达后生效" {
		t.Errorf("next = %v", body["next"])
	}
}

func TestWebSetIPValidation(t *testing.T) {
	_, ts := newTestWebServer(t)
	for _, req := range []map[string]any{
		{"lidar": "", "ip": "192.168.1.120"},
		{"lidar": "192.168.1.100", "ip": ""},
		{"lidar": "not-an-ip", "ip": "192.168.1.120"},
		{"lidar": "192.168.1.100", "ip": "192.168.1.300"},
		{"lidar": "192.168.1.100", "ip": "192.168.1.120", "mask": "bad"},
	} {
		resp, body := postJSON(t, ts.URL+"/api/set-ip", req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("set-ip(%v) = %d, want 400", req, resp.StatusCode)
			continue
		}
		errObj := body["error"].(map[string]any)
		if errObj["code"] != errCodeBadArgument {
			t.Errorf("set-ip(%v) code = %v, want %s", req, errObj["code"], errCodeBadArgument)
		}
		// user-facing text must not leak technical details
		msg := fmt.Sprint(errObj["message"])
		for _, tech := range []string{"IPv4", "net.", "parse", "udp", "ack"} {
			if strings.Contains(msg, tech) {
				t.Errorf("set-ip(%v) message %q leaks %q", req, msg, tech)
			}
		}
	}
}

func TestWebRebootAgainstFakeLidar(t *testing.T) {
	lidar := startFakeLidar(t, nil, func(req livox.CmdFrame) [][]byte {
		ack, err := livox.BuildFrame(livox.CmdIDRebootDevice, livox.CmdTypeAck,
			livox.SenderTypeLidar, req.SeqNum, []byte{0}, livox.ProtocolVersion)
		if err != nil {
			t.Errorf("BuildFrame: %v", err)
		}
		return [][]byte{ack}
	})
	_, ts := newTestWebServer(t)

	resp, body := postJSON(t, ts.URL+"/api/reboot", map[string]any{
		"lidar":    "127.0.0.1",
		"cmd_port": lidar.addr().Port,
	})
	if resp.StatusCode != http.StatusOK || body["ok"] != true {
		t.Fatalf("reboot = %d (body %v)", resp.StatusCode, body)
	}
}

func TestWebServesUI(t *testing.T) {
	_, ts := newTestWebServer(t)
	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
	bs, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(bs), "<div id=\"app\">") {
		t.Error("index.html does not look like the Vue app entry")
	}
}
