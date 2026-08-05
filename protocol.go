package main

// Wire protocol: binary WebSocket frames. Byte 0 = message type, bytes 1+ = payload.
//
// A single logical test run uses several WebSocket connections that share one
// session ID, chosen by the client and announced in the HELLO that must be the
// first frame on every connection:
//
//	role "control"  — exactly one per session. Carries PING/PONG for the whole
//	                  run (including *during* the load phases, which is how
//	                  latency-under-load is measured), the PROGRESS relay, and
//	                  the FINAL result. Claiming the control role claims the
//	                  server's single-test lock.
//	role "stream"   — 1, 4 or 8 per session. Carries bulk DOWN_DATA / UP_DATA
//	                  only. Keeping bulk traffic off the control connection is
//	                  what lets a ping get through while the link is saturated.
//	role "observer" — any number, never blocked by the test lock. Receives
//	                  OBSERVE events so a second screen (the desktop window)
//	                  can render the same live chart as the device under test.
//
// Transport seam for future WebRTC DataChannel mode (deliberately NOT
// implemented here):
//
//	The test logic reads and writes typed messages; it never touches
//	*websocket.Conn directly outside of wsConn. To add an unreliable transport
//	(which would give real packet loss and one-way jitter, neither of which TCP
//	can expose), define
//
//	    type Transport interface {
//	        ReadMsg() (byte, []byte, error)
//	        WriteMsg(byte, []byte) error
//	        Close() error
//	    }
//
//	implement it for both *wsConn and *webrtc.DataChannel, and swap the
//	concrete type in Session.streams. Add a signaling endpoint (e.g.
//	POST /rtc/offer) to negotiate the DataChannel; every message type below
//	stays byte-for-byte identical. Only the stream role needs the unreliable
//	transport — control stays on WebSocket so that results survive a lossy
//	link.
const (
	MsgPing      byte = 0x01 // C→S  control  payload: 8-byte float64 client timestamp
	MsgPong      byte = 0x02 // S→C  control  payload: echo of the PING payload
	MsgDownStart byte = 0x03 // C→S  stream   payload: JSON DownStartCfg
	MsgDownData  byte = 0x04 // S→C  stream   payload: bytes from the pre-allocated buffer
	MsgDownDone  byte = 0x05 // S→C  stream   payload: JSON DownDoneMsg (server-side diagnostic)
	MsgUpStart   byte = 0x06 // C→S  stream   payload: JSON UpStartCfg
	MsgUpData    byte = 0x07 // C→S  stream   payload: raw upload bytes
	MsgUpDone    byte = 0x08 // C→S  stream   payload: (empty)
	MsgResult    byte = 0x09 // S→C  control  payload: JSON UpResultMsg (aggregated over streams)
	MsgAbort     byte = 0x0A // both any      payload: (empty)
	MsgHello     byte = 0x0B // C→S  any      payload: JSON Hello — must be the first frame
	MsgHelloAck  byte = 0x0C // S→C  any      payload: JSON HelloAck
	MsgBusy      byte = 0x0D // S→C  any      payload: JSON Busy — another test holds the lock
	MsgProgress  byte = 0x0E // C→S  control  payload: JSON Progress, relayed to observers
	MsgObserve   byte = 0x0F // S→C  observer payload: JSON ObserveEvent
	MsgFinal     byte = 0x10 // C→S  control  payload: JSON FinalMsg
	MsgStored    byte = 0x11 // S→C  control  payload: JSON StoredMsg (run + previous run)
)

// Connection roles. Sent in Hello.Role.
const (
	RoleControl  = "control"
	RoleStream   = "stream"
	RoleObserver = "observer"
)

// Test phases, used to tag latency samples and drive both UIs.
const (
	PhaseIdle     = "idle"
	PhaseLatency  = "latency"
	PhaseDownload = "download"
	PhaseUpload   = "upload"
	PhaseDone     = "done"
)

// Hello is the first frame on every connection.
type Hello struct {
	SessionID string `json:"sessionId"`
	Role      string `json:"role"`
	Index     int    `json:"index"`   // stream index, 0-based
	Streams   int    `json:"streams"` // how many stream connections the client will open
	UA        string `json:"ua"`
}

// HelloAck accepts a connection into a session.
type HelloAck struct {
	OK        bool   `json:"ok"`
	Role      string `json:"role"`
	SessionID string `json:"sessionId"`
	Port      int    `json:"port"`
	Version   string `json:"version"`
}

// Busy rejects a second concurrent test. The client shows the message verbatim
// rather than reporting numbers that would be wrong.
type Busy struct {
	Message string `json:"message"`
	Since   string `json:"since,omitempty"`
	Peer    string `json:"peer,omitempty"`
}

// DownStartCfg asks one stream to send for DurationMs using ChunkBytes frames.
type DownStartCfg struct {
	DurationMs int `json:"durationMs"`
	ChunkBytes int `json:"chunkBytes"`
}

// DownDoneMsg is the server's own view of one download stream. The client's
// received-byte counter is the source of truth; this is kept only as a
// diagnostic, because WriteMessage returning means the bytes reached the
// kernel socket buffer, not the client.
type DownDoneMsg struct {
	Index      int          `json:"index"`
	TotalBytes int64        `json:"totalBytes"`
	ElapsedMs  int64        `json:"elapsedMs"`
	Stalls     []StallEvent `json:"stalls"`
}

// StallEvent records a single write that blocked long enough to mean the
// client stopped draining the socket.
type StallEvent struct {
	AtMs       int64 `json:"at_ms"`
	DurationMs int64 `json:"duration_ms"`
}

// UpStartCfg asks the server to receive on this stream for DurationMs.
type UpStartCfg struct {
	DurationMs int `json:"durationMs"`
}

// UpResultMsg is the aggregate of every upload stream in the session. Upload is
// measured server-side because that is the only end that knows what actually
// arrived.
type UpResultMsg struct {
	TotalBytes  int64    `json:"totalBytes"`
	ElapsedMs   int64    `json:"elapsedMs"`
	Streams     int      `json:"streams"`
	Samples     []Sample `json:"samples"`
	Stats       DirStats `json:"stats"`
	WarmupMs    int64    `json:"warmupMs"`
	SampleMs    int64    `json:"sampleMs"`
	Incomplete  bool     `json:"incomplete"`
	IncompleteR string   `json:"incompleteReason,omitempty"`
}

// Progress is sent ~4x/s by the control connection and relayed to observers so
// the desktop window can draw the same live chart as the phone.
type Progress struct {
	Phase     string  `json:"phase"`
	ElapsedMs int64   `json:"elapsedMs"`
	Mbps      float64 `json:"mbps"`
	RTTMs     float64 `json:"rttMs"`
	Fraction  float64 `json:"fraction"`
}

// FinalMsg carries the client's raw measurements at the end of a run.
//
// Deliberately raw: the client sends untrimmed sample windows and untrimmed
// round-trip times, and the server does all the trimming, percentile and
// grading maths. That keeps one tested implementation of the statistics rather
// than a Go copy and a JavaScript copy that can drift apart, and it means the
// phone, the desktop window and the history file cannot disagree about what a
// run means. Upload figures are not sent at all — the server measured those
// itself and keeps them on the session.
type FinalMsg struct {
	Streams    int   `json:"streams"`
	DurationMs int64 `json:"durationMs"`

	// DownloadSamples is the client's aggregate byte count per window, summed
	// across every stream. The client's counter is authoritative for download.
	DownloadSamples    []Sample `json:"downloadSamples"`
	DownloadTotalBytes int64    `json:"downloadTotalBytes"`
	// ServerDownloadBytes is what the server reported sending, summed over
	// streams. Kept only to detect and report a discrepancy.
	ServerDownloadBytes int64 `json:"serverDownloadBytes"`

	RTTIdle     []float64 `json:"rttIdle"`
	RTTDownload []float64 `json:"rttDownload"`
	RTTUpload   []float64 `json:"rttUpload"`

	Reliable bool     `json:"reliable"`
	Aborted  bool     `json:"aborted"`
	Caveats  []string `json:"caveats"`
	UA       string   `json:"ua"`
}

// StoredMsg returns the persisted run plus the previous run on the same
// network, if there is one.
type StoredMsg struct {
	Run      Run  `json:"run"`
	Previous *Run `json:"previous,omitempty"`
}

// ObserveEvent is pushed to observer connections.
type ObserveEvent struct {
	Type     string     `json:"type"` // state | progress | final | peers
	State    string     `json:"state,omitempty"`
	Peer     *PeerInfo  `json:"peer,omitempty"`
	Progress *Progress  `json:"progress,omitempty"`
	Stored   *StoredMsg `json:"stored,omitempty"`
	// Peers lists remote devices that have the page open but are not running a
	// test yet. The host screen uses it to say "phone connected" before anyone
	// presses start.
	Peers []PeerInfo `json:"peers,omitempty"`
}

// PeerInfo describes the device currently holding the test lock.
type PeerInfo struct {
	Addr    string `json:"addr"`
	UA      string `json:"ua"`
	Label   string `json:"label"`
	SinceMs int64  `json:"sinceMs"`
	Local   bool   `json:"local"`
}
