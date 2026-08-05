package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ── harness ──────────────────────────────────────────────────────────────────

type testRig struct {
	srv     *Server
	http    *httptest.Server
	wsURL   string
	history *HistoryStore
}

func newTestRig(t *testing.T) *testRig {
	t.Helper()
	content, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatalf("embedded assets: %v", err)
	}
	hist := NewHistoryStore(filepath.Join(t.TempDir(), "history.json"))
	srv := newServer(8080, 4, hist, NetworkIdentity{
		SSID: "TestNet", Subnet: "192.168.1.0/24", Key: "ssid:TestNet",
	}, content)

	ts := httptest.NewServer(srv.Mux())
	t.Cleanup(ts.Close)

	return &testRig{
		srv:     srv,
		http:    ts,
		wsURL:   "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws",
		history: hist,
	}
}

func dial(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	c, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		status := "no response"
		if resp != nil {
			status = resp.Status
		}
		t.Fatalf("dial %s: %v (%s)", wsURL, err, status)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func sendMsg(t *testing.T, c *websocket.Conn, typ byte, payload []byte) {
	t.Helper()
	buf := make([]byte, 1+len(payload))
	buf[0] = typ
	copy(buf[1:], payload)
	_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := c.WriteMessage(websocket.BinaryMessage, buf); err != nil {
		t.Fatalf("write msg 0x%02x: %v", typ, err)
	}
}

func sendJSONMsg(t *testing.T, c *websocket.Conn, typ byte, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sendMsg(t, c, typ, data)
}

func readMsg(t *testing.T, c *websocket.Conn) (byte, []byte) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("empty frame")
	}
	return data[0], data[1:]
}

func expectMsg(t *testing.T, c *websocket.Conn, want byte) []byte {
	t.Helper()
	got, payload := readMsg(t, c)
	if got != want {
		t.Fatalf("expected message 0x%02x, got 0x%02x (%s)", want, got, string(payload))
	}
	return payload
}

func handshake(t *testing.T, wsURL string, hello Hello) *websocket.Conn {
	t.Helper()
	c := dial(t, wsURL)
	sendJSONMsg(t, c, MsgHello, hello)
	return c
}

// ── origin and host policy ───────────────────────────────────────────────────

func TestCheckOrigin(t *testing.T) {
	rig := newTestRig(t)

	tests := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"private host, no origin", "192.168.1.5:8080", "", true},
		{"loopback host, no origin", "127.0.0.1:8080", "", true},
		{"private host and matching origin", "192.168.1.5:8080", "http://192.168.1.5:8080", true},
		{"private host, loopback origin", "127.0.0.1:8080", "http://localhost:8080", true},
		{"IPv6 ULA host", "[fd00::1]:8080", "http://[fd00::1]:8080", true},
		{"desktop shell origin", "127.0.0.1:8080", "wails://wails", true},
		{"desktop shell origin on windows", "127.0.0.1:8080", "http://wails.localhost", true},

		{"public host", "203.0.113.4:8080", "", false},
		{"hostname instead of IP", "lantest.local:8080", "", false},
		{"private host but hostile origin", "192.168.1.5:8080", "https://evil.example.com", false},
		{"private host, public origin over http", "192.168.1.5:8080", "http://203.0.113.4", false},
		{"private host, file origin", "192.168.1.5:8080", "file://", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/ws", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := rig.srv.checkOrigin(r); got != tc.want {
				t.Errorf("checkOrigin(host=%q, origin=%q) = %v, want %v",
					tc.host, tc.origin, got, tc.want)
			}
		})
	}
}

func TestIsWailsOrigin(t *testing.T) {
	for _, raw := range []string{"wails://wails", "wails://wails.localhost", "http://wails.localhost"} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if !isWailsOrigin(u) {
			t.Errorf("%q should be recognised as the desktop shell origin", raw)
		}
	}
	u, _ := url.Parse("https://wails.evil.com")
	if isWailsOrigin(u) {
		t.Error("a lookalike host must not be accepted")
	}
}

// ── handshake rules ──────────────────────────────────────────────────────────

func TestFirstFrameMustBeHello(t *testing.T) {
	rig := newTestRig(t)
	c := dial(t, rig.wsURL)
	sendMsg(t, c, MsgPing, []byte{1, 2, 3})

	payload := expectMsg(t, c, MsgBusy)
	var busy Busy
	if err := json.Unmarshal(payload, &busy); err != nil {
		t.Fatalf("unmarshal busy: %v", err)
	}
	if busy.Message == "" {
		t.Error("refusal must carry a message")
	}
}

func TestUnknownRoleRejected(t *testing.T) {
	rig := newTestRig(t)
	c := handshake(t, rig.wsURL, Hello{SessionID: "s1", Role: "hacker"})
	expectMsg(t, c, MsgBusy)
}

func TestStreamWithoutSessionRejected(t *testing.T) {
	rig := newTestRig(t)
	c := handshake(t, rig.wsURL, Hello{SessionID: "nosuch", Role: RoleStream, Index: 0})
	expectMsg(t, c, MsgBusy)
}

func TestStreamIndexOutOfRangeRejected(t *testing.T) {
	rig := newTestRig(t)
	control := handshake(t, rig.wsURL, Hello{SessionID: "s1", Role: RoleControl})
	expectMsg(t, control, MsgHelloAck)

	c := handshake(t, rig.wsURL, Hello{SessionID: "s1", Role: RoleStream, Index: 99})
	expectMsg(t, c, MsgBusy)
}

// A second simultaneous test must be refused outright. Two runs sharing the
// link would each measure half the bandwidth and both would be wrong.
func TestConcurrentTestIsRefused(t *testing.T) {
	rig := newTestRig(t)

	first := handshake(t, rig.wsURL, Hello{
		SessionID: "first", Role: RoleControl,
		UA: "Mozilla/5.0 (Linux; Android 14) Chrome/120.0 Mobile Safari/537.36",
	})
	expectMsg(t, first, MsgHelloAck)

	second := handshake(t, rig.wsURL, Hello{SessionID: "second", Role: RoleControl})
	payload := expectMsg(t, second, MsgBusy)

	var busy Busy
	if err := json.Unmarshal(payload, &busy); err != nil {
		t.Fatalf("unmarshal busy: %v", err)
	}
	if !strings.Contains(busy.Message, "desfășurare") {
		t.Errorf("busy message should explain the refusal, got %q", busy.Message)
	}
	if busy.Peer != "Android / Chrome" {
		t.Errorf("busy message should name the device holding the lock, got %q", busy.Peer)
	}

	// Once the first run lets go, the lock is available again.
	_ = first.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		third := handshake(t, rig.wsURL, Hello{SessionID: "third", Role: RoleControl})
		typ, _ := readMsg(t, third)
		if typ == MsgHelloAck {
			return
		}
		_ = third.Close()
		if time.Now().After(deadline) {
			t.Fatal("lock was never released after the first control connection closed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ── full cycle ───────────────────────────────────────────────────────────────

func TestFullTestCycle(t *testing.T) {
	if testing.Short() {
		t.Skip("full cycle test moves real bytes; skipped in -short mode")
	}
	rig := newTestRig(t)
	const sid = "cycle1"

	// 1. Control connection claims the test.
	control := handshake(t, rig.wsURL, Hello{
		SessionID: sid, Role: RoleControl, Streams: 1,
		UA: "Mozilla/5.0 (Linux; Android 14) Chrome/120.0 Mobile Safari/537.36",
	})
	ackPayload := expectMsg(t, control, MsgHelloAck)
	var ack HelloAck
	if err := json.Unmarshal(ackPayload, &ack); err != nil {
		t.Fatalf("unmarshal ack: %v", err)
	}
	if !ack.OK || ack.Role != RoleControl || ack.SessionID != sid {
		t.Fatalf("bad ack: %+v", ack)
	}

	// 2. Latency: the PONG payload must echo the PING payload byte for byte,
	//    because the client derives RTT from the timestamp it embedded.
	pingPayload := []byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48}
	sendMsg(t, control, MsgPing, pingPayload)
	pong := expectMsg(t, control, MsgPong)
	if string(pong) != string(pingPayload) {
		t.Fatalf("PONG payload %v does not echo PING payload %v", pong, pingPayload)
	}

	// 3. A data stream joins the same session.
	stream := handshake(t, rig.wsURL, Hello{SessionID: sid, Role: RoleStream, Index: 0})
	expectMsg(t, stream, MsgHelloAck)

	// 4. Download.
	sendJSONMsg(t, stream, MsgDownStart, DownStartCfg{DurationMs: 400, ChunkBytes: 65536})
	var clientBytes int64
	var frames int
	var downDone DownDoneMsg
	for {
		typ, payload := readMsg(t, stream)
		if typ == MsgDownData {
			clientBytes += int64(len(payload))
			frames++
			continue
		}
		if typ == MsgDownDone {
			if err := json.Unmarshal(payload, &downDone); err != nil {
				t.Fatalf("unmarshal down done: %v", err)
			}
			break
		}
		t.Fatalf("unexpected message 0x%02x during download", typ)
	}
	if frames == 0 || clientBytes == 0 {
		t.Fatal("download delivered nothing")
	}
	if downDone.TotalBytes == 0 {
		t.Error("server-side diagnostic byte count is zero")
	}
	if downDone.ElapsedMs < 300 {
		t.Errorf("download ended too early: %d ms", downDone.ElapsedMs)
	}
	if downDone.Stalls == nil {
		t.Error("stalls must serialise as an array, never null")
	}

	// 5. Upload, long enough that windows survive the warmup trim.
	const upDuration = 2500 * time.Millisecond
	sendJSONMsg(t, stream, MsgUpStart, UpStartCfg{DurationMs: int(upDuration.Milliseconds())})
	chunk := make([]byte, 16*1024)
	stop := time.Now().Add(upDuration)
	var sentBytes int64
	for time.Now().Before(stop) {
		sendMsg(t, stream, MsgUpData, chunk)
		sentBytes += int64(len(chunk))
		time.Sleep(2 * time.Millisecond)
	}
	sendMsg(t, stream, MsgUpDone, nil)

	// 6. The aggregated upload result arrives on the control connection.
	resultPayload := expectMsg(t, control, MsgResult)
	var result UpResultMsg
	if err := json.Unmarshal(resultPayload, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.TotalBytes == 0 {
		t.Fatal("server received no upload bytes")
	}
	if result.Streams != 1 {
		t.Errorf("streams: got %d, want 1", result.Streams)
	}
	if result.WarmupMs != WarmupMs {
		t.Errorf("result must record the warmup it discarded, got %d", result.WarmupMs)
	}
	if len(result.Samples) == 0 || result.Stats.Samples == 0 {
		t.Fatalf("no upload windows survived: %+v", result)
	}
	for _, s := range result.Samples {
		if s.AtMs <= WarmupMs {
			t.Errorf("sample at %d ms should have been trimmed as warmup", s.AtMs)
		}
	}
	if result.Stats.P50Mbps <= 0 {
		t.Errorf("upload p50 should be positive, got %v", result.Stats.P50Mbps)
	}

	// 7. FINAL carries raw measurements only; the server does the statistics.
	//
	//    Download windows are built so that exactly the first four (1000 ms) are
	//    warmup and must be discarded, the last one must be dropped as a ragged
	//    tail, and the four in between are the measurement. The server byte
	//    count is deliberately 10% above the client's to prove the mismatch is
	//    caught and that the client's figure is the one reported.
	downSamples := make([]Sample, 0, 9)
	for w := 0; w < 9; w++ {
		bytes := int64(15_000_000) // steady state
		if w < 4 {
			bytes = 4_000_000 // slow start
		}
		if w == 8 {
			bytes = 500_000 // ragged final window
		}
		downSamples = append(downSamples, Sample{
			Window: w,
			AtMs:   int64(w+1) * SampleWindow.Milliseconds(),
			Bytes:  bytes,
			Mbps:   Mbps(bytes, SampleWindow),
		})
	}
	const clientTotal = 100_000_000
	final := FinalMsg{
		Streams:             1,
		DurationMs:          12000,
		DownloadSamples:     downSamples,
		DownloadTotalBytes:  clientTotal,
		ServerDownloadBytes: 110_000_000,
		RTTIdle:             []float64{2, 3, 3, 3, 4},
		RTTDownload:         []float64{20, 30, 40, 44, 45},
		RTTUpload:           []float64{15, 20, 25, 27, 28},
		Reliable:            true,
		Caveats:             []string{},
		UA:                  "Mozilla/5.0 (Linux; Android 14) Chrome/120.0 Mobile Safari/537.36",
	}
	sendJSONMsg(t, control, MsgFinal, final)

	storedPayload := expectMsg(t, control, MsgStored)
	var stored StoredMsg
	if err := json.Unmarshal(storedPayload, &stored); err != nil {
		t.Fatalf("unmarshal stored: %v", err)
	}
	run := stored.Run
	if run.ID == "" {
		t.Error("stored run needs an ID")
	}
	if run.NetworkKey != "ssid:TestNet" || run.SSID != "TestNet" {
		t.Errorf("network identity not stamped: %+v", run)
	}
	if run.Client != "Android / Chrome" {
		t.Errorf("client label: got %q", run.Client)
	}
	if run.PacketLoss != packetLossNote {
		t.Errorf("packet loss must be reported as unmeasured, got %q", run.PacketLoss)
	}
	if run.WarmupMs != WarmupMs {
		t.Errorf("warmup not recorded on the run, got %d", run.WarmupMs)
	}
	// Four warmup windows and one ragged tail window must have been discarded,
	// leaving the four steady-state windows. If slow-start leaked in, p50 would
	// land well below 480 Mbps.
	if run.Download.Samples != 4 {
		t.Errorf("expected 4 measured download windows after trimming, got %d", run.Download.Samples)
	}
	almost(t, run.Download.P50Mbps, 480, "download p50 after warmup trim")
	if run.Download.TotalBytes != 60_000_000 {
		t.Errorf("measured-window bytes: got %d, want 60000000", run.Download.TotalBytes)
	}

	// Upload comes from what this server actually received, not from anything
	// the client claimed.
	if run.Upload.P50Mbps != result.Stats.P50Mbps || run.Upload.Samples != result.Stats.Samples {
		t.Errorf("upload figures should come from the server's own measurement: %+v vs %+v",
			run.Upload, result.Stats)
	}

	// idle p50 = 3, worst loaded p95 = 44.8 -> delta 41.8 -> grade C
	almost(t, run.Latency.Idle.P50Ms, 3, "idle p50")
	almost(t, run.Bufferbloat.DeltaMs, 41.8, "bufferbloat delta")
	if run.Bufferbloat.Grade != "C" {
		t.Errorf("bufferbloat grade: got %q, want C", run.Bufferbloat.Grade)
	}
	if !strings.Contains(run.Verdict, "bufferbloat moderat") {
		t.Errorf("verdict should name the bufferbloat level, got %q", run.Verdict)
	}
	if stored.Previous != nil {
		t.Error("first run on this network must not report a previous run")
	}

	// The 10% gap between the two byte counters must surface as a caveat.
	foundCaveat := false
	for _, c := range run.Caveats {
		if strings.Contains(c, "clientului") {
			foundCaveat = true
		}
	}
	if !foundCaveat {
		t.Errorf("byte-count mismatch above %.0f%% must be flagged, caveats: %v",
			downloadDeltaTolerance, run.Caveats)
	}
	if run.DownloadDeltaPct >= 0 {
		t.Errorf("client received less than the server sent, delta should be negative: %v", run.DownloadDeltaPct)
	}

	// 8. The run is readable over HTTP.
	resp, err := http.Get(rig.http.URL + "/api/history")
	if err != nil {
		t.Fatalf("GET /api/history: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Network NetworkIdentity `json:"network"`
		Runs    []Run           `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(body.Runs) != 1 {
		t.Fatalf("expected 1 stored run, got %d", len(body.Runs))
	}
	if body.Runs[0].ID != run.ID {
		t.Errorf("history holds a different run: %q vs %q", body.Runs[0].ID, run.ID)
	}
	if body.Network.SSID != "TestNet" {
		t.Errorf("history response should carry the network identity, got %+v", body.Network)
	}
}

// ── observers ────────────────────────────────────────────────────────────────

// nextObserve reads the next observer event of a given type. Presence updates
// ("peers") are emitted whenever any device opens or closes the page, so they
// can arrive between the events a test cares about.
func nextObserve(t *testing.T, c *websocket.Conn, wantType string) ObserveEvent {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var ev ObserveEvent
		if err := json.Unmarshal(expectMsg(t, c, MsgObserve), &ev); err != nil {
			t.Fatalf("unmarshal observe: %v", err)
		}
		if ev.Type == wantType {
			return ev
		}
	}
	t.Fatalf("no %q event arrived", wantType)
	return ObserveEvent{}
}

func TestObserverSeesTestLifecycle(t *testing.T) {
	rig := newTestRig(t)

	observer := handshake(t, rig.wsURL, Hello{Role: RoleObserver})
	expectMsg(t, observer, MsgHelloAck)

	ev := nextObserve(t, observer, "state")
	if ev.State != "idle" {
		t.Fatalf("a fresh observer should see an idle server, got %q", ev.State)
	}

	control := handshake(t, rig.wsURL, Hello{
		SessionID: "obs1", Role: RoleControl,
		UA: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Version/17.0 Mobile Safari/604.1",
	})
	expectMsg(t, control, MsgHelloAck)

	ev = nextObserve(t, observer, "state")
	if ev.State != "running" {
		t.Fatalf("observer should see the test start, got %q", ev.State)
	}
	if ev.Peer == nil || ev.Peer.Label != "iPhone / Safari" {
		t.Fatalf("observer should learn which device connected, got %+v", ev.Peer)
	}

	// Live progress from the device under test is relayed so a second screen
	// can draw the same chart.
	sendJSONMsg(t, control, MsgProgress, Progress{
		Phase: PhaseDownload, ElapsedMs: 1250, Mbps: 480, RTTMs: 42, Fraction: 0.4,
	})
	ev = nextObserve(t, observer, "progress")
	if ev.Progress == nil {
		t.Fatalf("expected a progress relay, got %+v", ev)
	}
	if ev.Progress.Mbps != 480 || ev.Progress.Phase != PhaseDownload {
		t.Errorf("progress relayed incorrectly: %+v", ev.Progress)
	}

	_ = control.Close()
	ev = nextObserve(t, observer, "state")
	if ev.State != "idle" {
		t.Errorf("observer should see the test finish, got %q", ev.State)
	}
}

// An observer must never be blocked by a running test.
func TestObserverIsNotBlockedByRunningTest(t *testing.T) {
	rig := newTestRig(t)

	control := handshake(t, rig.wsURL, Hello{SessionID: "busy", Role: RoleControl})
	expectMsg(t, control, MsgHelloAck)

	observer := handshake(t, rig.wsURL, Hello{Role: RoleObserver})
	expectMsg(t, observer, MsgHelloAck)

	ev := nextObserve(t, observer, "state")
	if ev.State != "running" || ev.Peer == nil {
		t.Errorf("a late observer should be told a test is already running, got %+v", ev)
	}
}

// A phone that merely opens the page must be announced, so the host screen can
// say "connected" before anyone presses start.
func TestObserverPresenceIsAnnounced(t *testing.T) {
	rig := newTestRig(t)

	watcher := handshake(t, rig.wsURL, Hello{Role: RoleObserver})
	expectMsg(t, watcher, MsgHelloAck)
	nextObserve(t, watcher, "state")

	// A loopback observer is the desktop window itself and is not reported.
	ev := nextObserve(t, watcher, "peers")
	if len(ev.Peers) != 0 {
		t.Errorf("loopback observers must not be listed as phones, got %+v", ev.Peers)
	}
}

// ── static endpoints ─────────────────────────────────────────────────────────

func TestConfigEndpoint(t *testing.T) {
	rig := newTestRig(t)
	resp, err := http.Get(rig.http.URL + "/config.js")
	if err != nil {
		t.Fatalf("GET /config.js: %v", err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])

	for _, want := range []string{"window.LANTEST=", `"port":8080`, `"defaultStreams":4`, `"warmupMs":1000`, `"sampleMs":250`, "TestNet"} {
		if !strings.Contains(body, want) {
			t.Errorf("config.js missing %q; body: %s", want, body)
		}
	}
}

func TestFrontendIsServed(t *testing.T) {
	rig := newTestRig(t)
	resp, err := http.Get(rig.http.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / returned %s", resp.Status)
	}
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(strings.ToLower(string(buf[:n])), "<!doctype html") {
		t.Errorf("embedded frontend not served; got %q", string(buf[:n]))
	}
}

func TestHistoryEndpointRejectsWrites(t *testing.T) {
	rig := newTestRig(t)
	resp, err := http.Post(rig.http.URL+"/api/history", "application/json", strings.NewReader("[]"))
	if err != nil {
		t.Fatalf("POST /api/history: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("history must be read-only over HTTP, got %s", resp.Status)
	}
}
