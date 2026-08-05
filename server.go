package main

import (
	"crypto/rand"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	maxChunkSize   = 256 * 1024
	defaultChunk   = 64 * 1024
	stallThreshold = 200 * time.Millisecond
	maxDuration    = 60 * time.Second
)

type Server struct {
	randomBuf []byte
	upgrader  websocket.Upgrader
}

type StallEvent struct {
	AtMs       int64 `json:"at_ms"`
	DurationMs int64 `json:"duration_ms"`
}

type DownStartCfg struct {
	DurationMs int `json:"durationMs"`
	ChunkBytes int `json:"chunkBytes"`
}

type DownDoneMsg struct {
	TotalBytes int64        `json:"totalBytes"`
	ElapsedMs  int64        `json:"elapsedMs"`
	Stalls     []StallEvent `json:"stalls"`
}

type UpStartCfg struct {
	DurationMs int `json:"durationMs"`
}

type UpSample struct {
	TimeMs int64   `json:"time_ms"`
	Mbps   float64 `json:"mbps"`
}

type UpResultMsg struct {
	TotalBytes int64      `json:"totalBytes"`
	ElapsedMs  int64      `json:"elapsedMs"`
	Samples    []UpSample `json:"samples"`
}

func newServer() *Server {
	buf := make([]byte, maxChunkSize)
	if _, err := rand.Read(buf); err != nil {
		log.Fatalf("failed to generate random buffer: %v", err)
	}
	return &Server{
		randomBuf: buf,
		upgrader: websocket.Upgrader{
			ReadBufferSize:    128 * 1024,
			WriteBufferSize:   128 * 1024,
			CheckOrigin:       func(r *http.Request) bool { return true },
			EnableCompression: false,
		},
	}
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade error: %v", err)
		return
	}
	defer conn.Close()
	log.Printf("client connected: %s", r.RemoteAddr)

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("read error: %v", err)
			}
			return
		}
		if len(msg) == 0 {
			continue
		}

		switch msg[0] {
		case MsgPing:
			s.handlePing(conn, msg)
		case MsgDownStart:
			s.handleDownload(conn, msg[1:])
		case MsgUpStart:
			s.handleUpload(conn, msg[1:])
		case MsgAbort:
			log.Printf("client aborted")
			return
		}
	}
}

func (s *Server) handlePing(conn *websocket.Conn, msg []byte) {
	resp := make([]byte, len(msg))
	copy(resp, msg)
	resp[0] = MsgPong
	conn.WriteMessage(websocket.BinaryMessage, resp)
}

func (s *Server) handleDownload(conn *websocket.Conn, payload []byte) {
	var cfg DownStartCfg
	if err := json.Unmarshal(payload, &cfg); err != nil {
		log.Printf("bad DOWN_START payload: %v", err)
		return
	}

	chunkSize := cfg.ChunkBytes
	if chunkSize <= 0 || chunkSize > maxChunkSize {
		chunkSize = defaultChunk
	}
	duration := clampDuration(cfg.DurationMs)

	// Build reusable message: type byte + chunk from pre-allocated random buffer
	msg := make([]byte, 1+chunkSize)
	msg[0] = MsgDownData
	copy(msg[1:], s.randomBuf[:chunkSize])

	startTime := time.Now()
	deadline := startTime.Add(duration)
	var totalBytes int64
	var stalls []StallEvent

	log.Printf("download: %dKB chunks for %v", chunkSize/1024, duration)

	for time.Now().Before(deadline) {
		writeStart := time.Now()
		if err := conn.WriteMessage(websocket.BinaryMessage, msg); err != nil {
			log.Printf("download write error: %v", err)
			return
		}
		totalBytes += int64(chunkSize)

		if wd := time.Since(writeStart); wd > stallThreshold {
			stalls = append(stalls, StallEvent{
				AtMs:       time.Since(startTime).Milliseconds(),
				DurationMs: wd.Milliseconds(),
			})
		}
	}

	elapsed := time.Since(startTime)
	done := DownDoneMsg{
		TotalBytes: totalBytes,
		ElapsedMs:  elapsed.Milliseconds(),
		Stalls:     stalls,
	}
	if done.Stalls == nil {
		done.Stalls = []StallEvent{}
	}
	sendJSON(conn, MsgDownDone, done)
	log.Printf("download done: %d MB in %v, %d stalls",
		totalBytes/1024/1024, elapsed.Round(time.Millisecond), len(stalls))
}

func (s *Server) handleUpload(conn *websocket.Conn, payload []byte) {
	var cfg UpStartCfg
	if err := json.Unmarshal(payload, &cfg); err != nil {
		log.Printf("bad UP_START payload: %v", err)
		return
	}

	duration := clampDuration(cfg.DurationMs)
	safetyDeadline := time.Now().Add(duration + 10*time.Second)

	startTime := time.Now()
	var totalBytes int64
	var samples []UpSample

	sampleStart := startTime
	var sampleBytes int64

	log.Printf("upload: receiving for up to %v", duration)

	for {
		conn.SetReadDeadline(safetyDeadline)
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("upload read ended: %v", err)
			break
		}
		if len(msg) == 0 {
			continue
		}

		switch msg[0] {
		case MsgUpData:
			n := int64(len(msg) - 1)
			totalBytes += n
			sampleBytes += n

			now := time.Now()
			if now.Sub(sampleStart) >= 250*time.Millisecond {
				elapsed := now.Sub(sampleStart)
				mbps := float64(sampleBytes*8) / elapsed.Seconds() / 1e6
				samples = append(samples, UpSample{
					TimeMs: now.Sub(startTime).Milliseconds(),
					Mbps:   mbps,
				})
				sampleStart = now
				sampleBytes = 0
			}

		case MsgUpDone:
			now := time.Now()
			if sampleBytes > 0 {
				elapsed := now.Sub(sampleStart)
				if elapsed > 0 {
					mbps := float64(sampleBytes*8) / elapsed.Seconds() / 1e6
					samples = append(samples, UpSample{
						TimeMs: now.Sub(startTime).Milliseconds(),
						Mbps:   mbps,
					})
				}
			}
			goto done

		case MsgAbort:
			log.Printf("upload aborted by client")
			goto done
		}
	}

done:
	conn.SetReadDeadline(time.Time{})

	elapsed := time.Since(startTime)
	result := UpResultMsg{
		TotalBytes: totalBytes,
		ElapsedMs:  elapsed.Milliseconds(),
		Samples:    samples,
	}
	if result.Samples == nil {
		result.Samples = []UpSample{}
	}
	sendJSON(conn, MsgResult, result)
	log.Printf("upload done: %d MB in %v, %d samples",
		totalBytes/1024/1024, elapsed.Round(time.Millisecond), len(samples))
}

func sendJSON(conn *websocket.Conn, msgType byte, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("marshal error: %v", err)
		return
	}
	msg := make([]byte, 1+len(data))
	msg[0] = msgType
	copy(msg[1:], data)
	conn.WriteMessage(websocket.BinaryMessage, msg)
}

func clampDuration(ms int) time.Duration {
	d := time.Duration(ms) * time.Millisecond
	if d <= 0 {
		return 10 * time.Second
	}
	if d > maxDuration {
		return maxDuration
	}
	return d
}
