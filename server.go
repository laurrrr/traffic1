package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	qrcode "github.com/skip2/go-qrcode"
)

const (
	version = "0.2.0"

	maxChunkSize = 256 * 1024
	defaultChunk = 64 * 1024

	minDuration     = 200 * time.Millisecond
	defaultDuration = 10 * time.Second
	maxDuration     = 60 * time.Second

	// stallThreshold is how long a single write may block before it counts as
	// the client having stopped draining the socket.
	stallThreshold = 200 * time.Millisecond

	// writeTimeout bounds every write. Without it a client that stops reading
	// (screen locked, walked out of range) pins the sending goroutine forever.
	writeTimeout = 10 * time.Second

	// helloTimeout is how long a fresh connection has to identify itself.
	helloTimeout = 15 * time.Second

	// connIdleTimeout bounds how long a connection may sit without any traffic.
	connIdleTimeout = 3 * time.Minute

	// uploadGrace is extra read time beyond the requested upload duration, so a
	// slow link can still deliver its final frames.
	uploadGrace = 15 * time.Second

	maxStreams = 8

	// maxManualDuration caps a run that has no fixed length. The operator ends a
	// manual run by pressing stop; this only exists so a client that vanishes
	// without saying so cannot pin a sending goroutine forever.
	maxManualDuration = 60 * time.Minute

	// manualIdleTimeout is how long a manual upload may go without a frame
	// before the server assumes the client is gone.
	manualIdleTimeout = 30 * time.Second

	// downloadDeltaTolerance is how far the server's byte count may drift from
	// the client's before the run is flagged. The client is authoritative.
	downloadDeltaTolerance = 5.0

	packetLossNote = "NEMĂSURAT (TCP ascunde retransmisiile)"

	busyMessage = "Un test este deja în desfășurare. Rezultatele a două teste simultane ar fi false, așa că acesta a fost refuzat. Încearcă din nou după ce se termină."
)

// wsConn serialises writes to a websocket. gorilla connections allow exactly
// one concurrent writer, and results are written from a different goroutine
// than the one that reads, so every write goes through here.
type wsConn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func newWSConn(ws *websocket.Conn) *wsConn { return &wsConn{ws: ws} }

func (c *wsConn) writeRaw(b []byte, timeout time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if timeout > 0 {
		_ = c.ws.SetWriteDeadline(time.Now().Add(timeout))
	} else {
		_ = c.ws.SetWriteDeadline(time.Time{})
	}
	return c.ws.WriteMessage(websocket.BinaryMessage, b)
}

func (c *wsConn) write(t byte, payload []byte) error {
	return c.writeWithTimeout(t, payload, writeTimeout)
}

func (c *wsConn) writeWithTimeout(t byte, payload []byte, timeout time.Duration) error {
	buf := make([]byte, 1+len(payload))
	buf[0] = t
	copy(buf[1:], payload)
	return c.writeRaw(buf, timeout)
}

func (c *wsConn) writeJSON(t byte, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.write(t, data)
}

func (c *wsConn) close() { _ = c.ws.Close() }

// Session is one logical test run: a control connection plus its data streams.
// Only one may exist at a time.
type Session struct {
	ID      string
	Peer    PeerInfo
	control *wsConn
	started time.Time

	mu        sync.Mutex
	streams   map[int]*wsConn
	upStarted bool
	upStart   time.Time
	upActive  int
	upSamples map[int][]Sample
	upTotal   int64
	upFrames  int64
	upFinal   bool
	upStats   DirStats
	stopped   bool
}

// requestStop ends a manual run. The sending loop polls this between frames, so
// a stop takes effect within one frame rather than waiting out a deadline.
func (sess *Session) requestStop() {
	sess.mu.Lock()
	sess.stopped = true
	sess.mu.Unlock()
}

func (sess *Session) stopRequested() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.stopped
}

// uploadResult returns the upload figures the server measured for this session.
// They are read from here rather than from the client's FINAL message: the
// receiving end is the only one that knows what actually arrived.
func (sess *Session) uploadResult() (DirStats, int64, int64) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.upStats, sess.upTotal, sess.upFrames
}

// Server owns the single-test lock, the observer set and the persisted history.
type Server struct {
	randomBuf      []byte
	upgrader       websocket.Upgrader
	port           int
	defaultStreams int
	history        *HistoryStore
	network        NetworkIdentity
	webFS          fs.FS

	mu        sync.Mutex
	active    *Session
	observers map[*wsConn]PeerInfo
	lanURLs   []string
}

func newServer(port, defaultStreams int, hist *HistoryStore, network NetworkIdentity, webFS fs.FS) *Server {
	// One random buffer, allocated once. Generating randomness inside the send
	// loop would measure the CSPRNG rather than the network.
	buf := make([]byte, maxChunkSize)
	if _, err := rand.Read(buf); err != nil {
		log.Fatalf("failed to generate random buffer: %v", err)
	}
	if defaultStreams <= 0 || defaultStreams > maxStreams {
		defaultStreams = 4
	}

	s := &Server{
		randomBuf:      buf,
		port:           port,
		defaultStreams: defaultStreams,
		history:        hist,
		network:        network,
		webFS:          webFS,
		observers:      make(map[*wsConn]PeerInfo),
	}
	s.upgrader = websocket.Upgrader{
		ReadBufferSize:    128 * 1024,
		WriteBufferSize:   128 * 1024,
		EnableCompression: false, // compressing random data wastes CPU and skews results
		CheckOrigin:       s.checkOrigin,
	}
	return s
}

// checkOrigin refuses anything that is not a private LAN or loopback address.
// The Host check enforces the binding policy; the Origin check is what actually
// stops a page on the public internet from driving this server through the
// user's browser.
func (s *Server) checkOrigin(r *http.Request) bool {
	if !isPrivateHost(hostOnly(r.Host)) {
		log.Printf("rejected websocket: non-private Host %q", r.Host)
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No Origin means a non-browser client (our own tests, curl, a future
		// native client). The Host check above already applies.
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if isWailsOrigin(u) {
		return true
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		log.Printf("rejected websocket: unsupported Origin scheme %q", origin)
		return false
	}
	if !isPrivateHost(hostOnly(u.Host)) {
		log.Printf("rejected websocket: non-private Origin %q", origin)
		return false
	}
	return true
}

// isWailsOrigin recognises the scheme the desktop shell serves its assets from.
// The window is local by construction; it is not reachable from the network.
func isWailsOrigin(u *url.URL) bool {
	if u.Scheme == "wails" {
		return true
	}
	h := hostOnly(u.Host)
	return h == "wails.localhost" || h == "wails"
}

// Mux returns the HTTP handler. The same handler is served on every LAN
// listener, on loopback, and to the desktop window, so the frontend is byte
// for byte identical everywhere.
func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(s.webFS)))
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/config.js", s.handleConfig)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/urls", s.handleURLs)
	mux.HandleFunc("/qr.png", s.handleQR)
	return mux
}

// SetLANURLs records the addresses a phone can use to reach this server, so the
// host screen can list them and render a QR code.
func (s *Server) SetLANURLs(urls []string) {
	s.mu.Lock()
	s.lanURLs = append([]string(nil), urls...)
	s.mu.Unlock()
}

func (s *Server) urls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lanURLs...)
}

func (s *Server) handleURLs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"urls": s.urls()})
}

// handleQR renders the LAN URL as a QR code. Doing this server-side means the
// frontend needs no QR library, which keeps the "zero CDN, no build step"
// promise without shipping a few thousand lines of encoder in the page.
func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	urls := s.urls()
	if len(urls) == 0 {
		http.Error(w, "no LAN URL available", http.StatusNotFound)
		return
	}
	target := urls[0]
	if raw := r.URL.Query().Get("i"); raw != "" {
		if i, err := strconv.Atoi(raw); err == nil && i >= 0 && i < len(urls) {
			target = urls[i]
		}
	}
	png, err := qrcode.Encode(target, qrcode.Medium, 512)
	if err != nil {
		http.Error(w, "qr encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg := map[string]any{
		"port":           s.port,
		"defaultStreams": s.defaultStreams,
		"maxStreams":     maxStreams,
		"version":        version,
		"warmupMs":       WarmupMs,
		"sampleMs":       SampleWindow.Milliseconds(),
		"network":        s.network,
		"packetLoss":     packetLossNote,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		http.Error(w, "config error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, "window.LANTEST=%s;\n", data)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runs, err := s.history.Load()
	if err != nil {
		log.Printf("history load: %v", err)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"network": s.network,
		"runs":    runs,
	})
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade error: %v", err)
		return
	}
	c := newWSConn(ws)
	defer c.close()

	_ = ws.SetReadDeadline(time.Now().Add(helloTimeout))
	_, msg, err := ws.ReadMessage()
	if err != nil || len(msg) == 0 || msg[0] != MsgHello {
		_ = c.writeJSON(MsgBusy, Busy{Message: "prima cerere trebuie să fie HELLO"})
		return
	}
	var hello Hello
	if err := json.Unmarshal(msg[1:], &hello); err != nil {
		_ = c.writeJSON(MsgBusy, Busy{Message: "HELLO invalid"})
		return
	}
	hello.SessionID = sanitizeID(hello.SessionID)
	if hello.UA == "" {
		hello.UA = r.Header.Get("User-Agent")
	}

	switch hello.Role {
	case RoleObserver:
		s.serveObserver(c, hello, r)
	case RoleControl:
		s.serveControl(c, hello, r)
	case RoleStream:
		s.serveStream(c, hello)
	default:
		_ = c.writeJSON(MsgBusy, Busy{Message: "rol necunoscut: " + hello.Role})
	}
}

// serveControl runs the connection that owns the test. Bulk data never touches
// it, which is what lets PING get an answer while the link is saturated.
func (s *Server) serveControl(c *wsConn, hello Hello, r *http.Request) {
	peer := PeerInfo{
		Addr:  hostOnly(r.RemoteAddr),
		UA:    hello.UA,
		Label: describeUA(hello.UA),
	}
	peer.Local = isLoopbackAddr(peer.Addr)

	sess, ok := s.claim(hello.SessionID, peer, c)
	if !ok {
		busy := Busy{Message: busyMessage}
		if cur := s.currentPeer(); cur != nil {
			busy.Peer = cur.Label
		}
		_ = c.writeJSON(MsgBusy, busy)
		log.Printf("refused concurrent test from %s", peer.Addr)
		return
	}
	defer s.release(sess)

	log.Printf("test claimed by %s (%s), session %s", peer.Addr, peer.Label, sess.ID)
	_ = c.writeJSON(MsgHelloAck, HelloAck{
		OK: true, Role: RoleControl, SessionID: sess.ID, Port: s.port, Version: version,
	})
	p := sess.Peer
	s.broadcast(ObserveEvent{Type: "state", State: "running", Peer: &p})

	for {
		_ = c.ws.SetReadDeadline(time.Now().Add(connIdleTimeout))
		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		if len(msg) == 0 {
			continue
		}
		switch msg[0] {
		case MsgPing:
			resp := make([]byte, len(msg))
			copy(resp, msg)
			resp[0] = MsgPong
			if err := c.writeRaw(resp, writeTimeout); err != nil {
				return
			}
		case MsgProgress:
			var pr Progress
			if json.Unmarshal(msg[1:], &pr) == nil {
				peerCopy := sess.Peer
				s.broadcast(ObserveEvent{Type: "progress", Progress: &pr, Peer: &peerCopy})
			}
		case MsgFinal:
			s.handleFinal(sess, c, msg[1:])
		case MsgStop:
			// Manual run: the operator pressed stop. The load loops poll this.
			sess.requestStop()
			log.Printf("session %s: stop requested", sess.ID)
		case MsgAbort:
			log.Printf("session %s aborted by client", sess.ID)
			return
		}
	}
}

// serveStream runs one bulk data connection belonging to an existing session.
func (s *Server) serveStream(c *wsConn, hello Hello) {
	sess := s.lookup(hello.SessionID)
	if sess == nil {
		_ = c.writeJSON(MsgBusy, Busy{Message: "sesiune necunoscută sau deja încheiată"})
		return
	}
	idx := hello.Index
	if idx < 0 || idx >= maxStreams {
		_ = c.writeJSON(MsgBusy, Busy{Message: "index de stream invalid"})
		return
	}
	sess.addStream(idx, c)
	defer sess.removeStream(idx)

	_ = c.writeJSON(MsgHelloAck, HelloAck{
		OK: true, Role: RoleStream, SessionID: sess.ID, Port: s.port, Version: version,
	})

	for {
		_ = c.ws.SetReadDeadline(time.Now().Add(connIdleTimeout))
		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		if len(msg) == 0 {
			continue
		}
		switch msg[0] {
		case MsgDownStart:
			var cfg DownStartCfg
			if json.Unmarshal(msg[1:], &cfg) != nil {
				continue
			}
			s.sendDownload(sess, c, idx, cfg)
		case MsgUpStart:
			var cfg UpStartCfg
			if json.Unmarshal(msg[1:], &cfg) != nil {
				continue
			}
			s.recvUpload(sess, c, idx, cfg)
		case MsgAbort:
			return
		}
	}
}

func (s *Server) serveObserver(c *wsConn, hello Hello, r *http.Request) {
	peer := PeerInfo{
		Addr:    hostOnly(r.RemoteAddr),
		UA:      hello.UA,
		Label:   describeUA(hello.UA),
		SinceMs: time.Now().UnixMilli(),
	}
	peer.Local = isLoopbackAddr(peer.Addr)

	s.mu.Lock()
	s.observers[c] = peer
	active := s.active
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.observers, c)
		s.mu.Unlock()
		s.broadcastPeers()
	}()

	_ = c.writeJSON(MsgHelloAck, HelloAck{
		OK: true, Role: RoleObserver, Port: s.port, Version: version,
	})
	ev := ObserveEvent{Type: "state", State: "idle"}
	if active != nil {
		p := active.Peer
		ev.State = "running"
		ev.Peer = &p
	}
	_ = c.writeJSON(MsgObserve, ev)
	s.broadcastPeers()

	// Observers only listen; they send PING as a keepalive so the idle timeout
	// does not close a window that is simply waiting for a phone.
	for {
		_ = c.ws.SetReadDeadline(time.Now().Add(connIdleTimeout))
		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		if len(msg) > 0 && msg[0] == MsgPing {
			resp := make([]byte, len(msg))
			copy(resp, msg)
			resp[0] = MsgPong
			if err := c.writeRaw(resp, writeTimeout); err != nil {
				return
			}
		}
	}
}

// sendDownload streams to one connection until the deadline.
//
// The byte count returned here is a diagnostic only. WriteMessage returning nil
// means the kernel accepted the bytes into the socket buffer, not that they
// reached the client, so the client's received-byte counter is the figure that
// gets reported.
func (s *Server) sendDownload(sess *Session, c *wsConn, idx int, cfg DownStartCfg) {
	chunk := cfg.ChunkBytes
	if chunk <= 0 || chunk > maxChunkSize {
		chunk = defaultChunk
	}

	// Built once per phase from the pre-allocated buffer, then reused for every
	// write in the loop below.
	frame := make([]byte, 1+chunk)
	frame[0] = MsgDownData
	copy(frame[1:], s.randomBuf[:chunk])

	start := time.Now()
	deadline := start.Add(clampDuration(cfg.DurationMs))
	safetyCap := start.Add(maxManualDuration)
	var total, frames int64
	stalls := []StallEvent{}

	for {
		if cfg.Manual {
			// Ends when the operator says so. The cap is only a backstop for a
			// client that disappears without sending stop.
			if sess.stopRequested() || time.Now().After(safetyCap) {
				break
			}
		} else if !time.Now().Before(deadline) {
			break
		}

		writeStart := time.Now()
		if err := c.writeRaw(frame, writeTimeout); err != nil {
			log.Printf("download stream %d write error: %v", idx, err)
			return
		}
		total += int64(chunk)
		frames++
		if wd := time.Since(writeStart); wd > stallThreshold {
			stalls = append(stalls, StallEvent{
				AtMs:       time.Since(start).Milliseconds(),
				DurationMs: wd.Milliseconds(),
			})
		}
	}

	_ = c.writeJSON(MsgDownDone, DownDoneMsg{
		Index:      idx,
		TotalBytes: total,
		Frames:     frames,
		FrameBytes: chunk,
		ElapsedMs:  time.Since(start).Milliseconds(),
		Stalls:     stalls,
	})
}

// recvUpload consumes one upload stream, bucketing bytes into fixed windows
// aligned to a start time shared by every stream in the session. Aligned
// windows are what makes the per-stream series addable afterwards.
func (s *Server) recvUpload(sess *Session, c *wsConn, idx int, cfg UpStartCfg) {
	start := sess.beginUpload()
	hardDeadline := time.Now().Add(clampDuration(cfg.DurationMs) + uploadGrace)

	// A manual run has no known length, so the read deadline rolls forward on
	// every frame instead: the stream stays open as long as data keeps coming.
	nextDeadline := func() time.Time {
		if cfg.Manual {
			return time.Now().Add(manualIdleTimeout)
		}
		return hardDeadline
	}

	samples := []Sample{}
	var winBytes, total, frames int64
	cur := 0

	// closeWindows emits every window from cur up to (but excluding) upTo. Gaps
	// are emitted as zero-byte windows because a gap really is a period where
	// this stream delivered nothing.
	closeWindows := func(upTo int) {
		for w := cur; w < upTo; w++ {
			b := int64(0)
			if w == cur {
				b = winBytes
			}
			samples = append(samples, Sample{
				Window: w,
				AtMs:   int64(w+1) * SampleWindow.Milliseconds(),
				Bytes:  b,
				Mbps:   Mbps(b, SampleWindow),
			})
		}
		winBytes = 0
		cur = upTo
	}

	for {
		_ = c.ws.SetReadDeadline(nextDeadline())
		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			break
		}
		if len(msg) == 0 {
			continue
		}
		switch msg[0] {
		case MsgUpData:
			n := int64(len(msg) - 1)
			total += n
			frames++
			if w := int(time.Since(start) / SampleWindow); w > cur {
				closeWindows(w)
			}
			winBytes += n
		case MsgUpDone, MsgAbort:
			// The final partial window is dropped rather than divided by a full
			// window width, which would report a throughput collapse that never
			// happened.
			sess.endUpload(idx, samples, total, frames)
			return
		}
	}
	sess.endUpload(idx, samples, total, frames)
}

func (s *Server) handleFinal(sess *Session, c *wsConn, payload []byte) {
	var fm FinalMsg
	if err := json.Unmarshal(payload, &fm); err != nil {
		log.Printf("bad FINAL payload: %v", err)
		return
	}

	caveats := append([]string{}, fm.Caveats...)

	// Download: trim slow-start off the head and the ragged final window off the
	// tail, then summarise. Doing it here rather than in the browser keeps one
	// tested implementation of these rules.
	downMeasured := TrimWarmup(TrimTail(fm.DownloadSamples), WarmupMs)
	download := Summarize(downMeasured, fm.Streams, WarmupMs)

	// Upload was measured by this server, not reported by the client.
	upload, uploadBytes, uploadFrames := sess.uploadResult()

	mode := fm.Mode
	if mode != ModeManual {
		mode = ModeAuto
	}
	direction := fm.Direction
	switch direction {
	case DirectionDownload, DirectionUpload, DirectionBoth:
	default:
		direction = DirectionBoth
	}

	run := Run{
		SchemaVersion: historySchemaVersion,
		ID:            newRunID(),
		Timestamp:     time.Now(),
		NetworkKey:    s.network.Key,
		SSID:          s.network.SSID,
		Subnet:        s.network.Subnet,
		Streams:       fm.Streams,
		DurationMs:    fm.DurationMs,
		Client:        describeUA(fm.UA),
		ClientAddr:    sess.Peer.Addr,
		Mode:          mode,
		Direction:     direction,
		Frames: FrameStats{
			DownloadCount: fm.DownloadFrames,
			UploadCount:   uploadFrames,
			DownloadBytes: fm.DownloadTotalBytes,
			UploadBytes:   uploadBytes,
			FrameBytes:    fm.ChunkBytes,
		},
		Download: download,
		Upload:   upload,
		Latency: LatencyReport{
			Idle:           withSent(SummarizeLatency(fm.RTTIdle), fm.PingsIdle),
			LoadedDownload: withSent(SummarizeLatency(fm.RTTDownload), fm.PingsDownload),
			LoadedUpload:   withSent(SummarizeLatency(fm.RTTUpload), fm.PingsUpload),
			Scheduling:     SummarizeLatency(fm.SchedulingLag),
		},
		Reliable:   fm.Reliable,
		Aborted:    fm.Aborted,
		PacketLoss: packetLossNote,
		WarmupMs:   WarmupMs,
	}

	if fm.ServerDownloadBytes > 0 && fm.DownloadTotalBytes > 0 {
		run.ServerDownload = &DirStats{TotalBytes: fm.ServerDownloadBytes, Streams: fm.Streams}
		run.DownloadDeltaPct = DeltaPct(fm.DownloadTotalBytes, fm.ServerDownloadBytes)
		if math.Abs(run.DownloadDeltaPct) > downloadDeltaTolerance {
			caveats = append(caveats, fmt.Sprintf(
				"Serverul a trimis cu %.1f%% mai mult decât a primit clientul; se raportează cifra clientului.",
				-run.DownloadDeltaPct))
		}
	}

	run.Bufferbloat = ComputeBufferbloat(run.Latency, math.Max(download.P95Mbps, upload.P95Mbps))
	if !run.Bufferbloat.Trustworthy && run.Bufferbloat.Grade != "?" {
		// The client could not run its own code promptly, so what it timed
		// includes its own scheduling delay. Saying "severe bufferbloat" here
		// would be blaming the network for the browser.
		if run.Bufferbloat.ImpliedBufferBytes > maxPlausibleBufferBytes {
			caveats = append(caveats, fmt.Sprintf(
				"Creșterea de latență măsurată (%s) ar cere %s de tampon în rețea la %s. "+
					"Nu există așa ceva: întârzierea s-a acumulat în coada de mesaje a "+
					"clientului, care nu face față acestei viteze. Testează de pe un telefon, pe Wi-Fi.",
				FormatMs(run.Bufferbloat.DeltaMs),
				FormatBytes(run.Bufferbloat.ImpliedBufferBytes),
				FormatMbps(math.Max(download.P95Mbps, upload.P95Mbps))))
		} else if run.Bufferbloat.AnsweredRatio < minAnsweredRatio {
			caveats = append(caveats, fmt.Sprintf(
				"Doar %.0f%% dintre pachetele de latență au primit răspuns în timpul încărcării; "+
					"legătura e prea rapidă pentru acest dispozitiv, iar cifra sub sarcină "+
					"măsoară stiva lui de rețea, nu bufferele din cale.",
				run.Bufferbloat.AnsweredRatio*100))
		} else {
			caveats = append(caveats, fmt.Sprintf(
				"Firul principal al browserului a fost blocat până la %s în timpul testului; "+
					"latența sub sarcină include această întârziere, nu doar rețeaua.",
				FormatMs(run.Bufferbloat.SchedulingLagMs)))
		}
		run.Reliable = false
	}
	run.Verdict = BuildVerdict(run.Download, run.Upload, run.Bufferbloat, run.Direction, run.Aborted)
	run.Caveats = caveats

	stored, previous, err := s.history.Append(run)
	if err != nil {
		log.Printf("history append: %v", err)
	}
	msg := StoredMsg{Run: stored, Previous: previous}
	_ = c.writeJSON(MsgStored, msg)

	peer := sess.Peer
	s.broadcast(ObserveEvent{Type: "final", Stored: &msg, Peer: &peer})
	log.Printf("run %s stored: %s", stored.ID, stored.Verdict)
}

// claim takes the single-test lock. A second caller is refused outright rather
// than being allowed to produce numbers that would be wrong for both.
func (s *Server) claim(id string, peer PeerInfo, control *wsConn) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		return nil, false
	}
	if id == "" {
		id = newRunID()
	}
	peer.SinceMs = time.Now().UnixMilli()
	sess := &Session{
		ID:        id,
		Peer:      peer,
		control:   control,
		started:   time.Now(),
		streams:   make(map[int]*wsConn),
		upSamples: make(map[int][]Sample),
	}
	s.active = sess
	return sess, true
}

func (s *Server) release(sess *Session) {
	s.mu.Lock()
	if s.active == sess {
		s.active = nil
	}
	s.mu.Unlock()
	sess.closeStreams()
	s.broadcast(ObserveEvent{Type: "state", State: "idle"})
	log.Printf("session %s released", sess.ID)
}

func (s *Server) lookup(id string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil && s.active.ID == id {
		return s.active
	}
	return nil
}

func (s *Server) currentPeer() *PeerInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return nil
	}
	p := s.active.Peer
	return &p
}

// observerPeers lists remote devices with the page open. Loopback observers are
// the desktop window itself and are not interesting to report.
func (s *Server) observerPeers() []PeerInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []PeerInfo{}
	for _, p := range s.observers {
		if !p.Local {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) broadcastPeers() {
	s.broadcast(ObserveEvent{Type: "peers", Peers: s.observerPeers()})
}

func (s *Server) broadcast(ev ObserveEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	s.mu.Lock()
	obs := make([]*wsConn, 0, len(s.observers))
	for c := range s.observers {
		obs = append(obs, c)
	}
	s.mu.Unlock()
	for _, c := range obs {
		// Short timeout: a stalled observer must never hold up a running test.
		_ = c.writeWithTimeout(MsgObserve, data, 2*time.Second)
	}
}

func (sess *Session) addStream(idx int, c *wsConn) {
	sess.mu.Lock()
	sess.streams[idx] = c
	sess.mu.Unlock()
}

func (sess *Session) removeStream(idx int) {
	sess.mu.Lock()
	delete(sess.streams, idx)
	sess.mu.Unlock()
}

func (sess *Session) closeStreams() {
	sess.mu.Lock()
	conns := make([]*wsConn, 0, len(sess.streams))
	for _, c := range sess.streams {
		conns = append(conns, c)
	}
	sess.streams = make(map[int]*wsConn)
	sess.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
}

// beginUpload registers a stream as uploading and returns the shared phase
// start time, so every stream buckets into the same windows.
func (sess *Session) beginUpload() time.Time {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.upStarted {
		sess.upStarted = true
		sess.upStart = time.Now()
	}
	sess.upActive++
	return sess.upStart
}

// endUpload records a stream's samples. The last stream to finish aggregates
// everything and sends the result on the control connection.
func (sess *Session) endUpload(idx int, samples []Sample, total, frames int64) {
	sess.mu.Lock()
	sess.upSamples[idx] = samples
	sess.upTotal += total
	sess.upFrames += frames
	if sess.upActive > 0 {
		sess.upActive--
	}
	last := sess.upActive == 0 && !sess.upFinal
	if !last {
		sess.mu.Unlock()
		return
	}
	sess.upFinal = true

	perStream := make([][]Sample, 0, len(sess.upSamples))
	for _, series := range sess.upSamples {
		perStream = append(perStream, series)
	}
	streams := len(sess.upSamples)
	upTotal := sess.upTotal
	upFrames := sess.upFrames
	start := sess.upStart
	control := sess.control
	sess.mu.Unlock()

	aggregate := TrimTail(AggregateSamples(perStream))
	measured := TrimWarmup(aggregate, WarmupMs)
	stats := Summarize(measured, streams, WarmupMs)

	sess.mu.Lock()
	sess.upStats = stats
	sess.mu.Unlock()

	res := UpResultMsg{
		TotalBytes: upTotal,
		Frames:     upFrames,
		ElapsedMs:  time.Since(start).Milliseconds(),
		Streams:    streams,
		Samples:    measured,
		Stats:      stats,
		WarmupMs:   WarmupMs,
		SampleMs:   SampleWindow.Milliseconds(),
	}
	if len(measured) == 0 {
		res.Incomplete = true
		res.IncompleteR = "prea puține ferestre complete după eliminarea warmup-ului"
	}
	if control != nil {
		_ = control.writeJSON(MsgResult, res)
	}
	log.Printf("upload done: %d MB over %d streams, %d windows measured",
		upTotal/1024/1024, streams, len(measured))
}

// isLoopbackAddr reports whether an address belongs to this machine, which is
// how the desktop window is told apart from a phone on the LAN.
func isLoopbackAddr(addr string) bool {
	if addr == "" {
		return false
	}
	ip := net.ParseIP(strings.Trim(addr, "[]"))
	return ip != nil && ip.IsLoopback()
}

// withSent records how many pings a phase issued alongside how many came back.
func withSent(st LatencyStats, sent int) LatencyStats {
	st.Sent = sent
	return st
}

// sanitizeID keeps a client-supplied session ID to a harmless shape.
func sanitizeID(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}
