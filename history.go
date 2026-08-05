package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// historySchemaVersion is bumped whenever the stored shape changes. Readers
// must ignore runs they do not understand rather than guessing.
const historySchemaVersion = 1

// maxHistoryRuns caps the file so it stays small enough to read on every page
// load. Oldest runs are dropped first.
const maxHistoryRuns = 200

// Run is one persisted test result.
type Run struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	Timestamp     time.Time `json:"timestamp"`

	NetworkKey string `json:"network_key"`
	SSID       string `json:"ssid,omitempty"`
	Subnet     string `json:"subnet,omitempty"`

	Streams    int    `json:"streams"`
	DurationMs int64  `json:"duration_ms"`
	Client     string `json:"client"`
	ClientAddr string `json:"client_addr,omitempty"`

	Download DirStats `json:"download"`
	Upload   DirStats `json:"upload"`

	// ServerDownload is the server's own byte count for the download phase.
	// The client's counter is authoritative; this is kept for diagnosis only,
	// because a write returning on the server just means the kernel accepted
	// the bytes.
	ServerDownload   *DirStats `json:"server_download,omitempty"`
	DownloadDeltaPct float64   `json:"download_delta_pct"`

	Latency     LatencyReport `json:"latency"`
	Bufferbloat Bufferbloat   `json:"bufferbloat"`

	Verdict    string   `json:"verdict"`
	Reliable   bool     `json:"reliable"`
	Aborted    bool     `json:"aborted"`
	Caveats    []string `json:"caveats"`
	PacketLoss string   `json:"packet_loss"`
	WarmupMs   int64    `json:"warmup_discarded_ms"`
}

// HistoryStore persists runs as a JSON array in the user's config directory.
// Writes are serialised and go through a temp file so a crash mid-write cannot
// leave a truncated history behind.
type HistoryStore struct {
	path string
	mu   sync.Mutex
}

// NewHistoryStore returns a store backed by path.
func NewHistoryStore(path string) *HistoryStore {
	return &HistoryStore{path: path}
}

// Path reports where runs are stored.
func (h *HistoryStore) Path() string { return h.path }

// defaultHistoryPath is <user config dir>/lantest/history.json.
func defaultHistoryPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lantest", "history.json"), nil
}

// Load returns every stored run, newest first. A missing file is not an error.
func (h *HistoryStore) Load() ([]Run, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.loadLocked()
}

func (h *HistoryStore) loadLocked() ([]Run, error) {
	data, err := os.ReadFile(h.path)
	if os.IsNotExist(err) {
		return []Run{}, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []Run
	if err := json.Unmarshal(data, &runs); err != nil {
		// A corrupt history must not take the tool down; report it and start
		// fresh rather than refusing to run.
		return []Run{}, fmt.Errorf("history file unreadable, ignoring: %w", err)
	}
	kept := runs[:0]
	for _, r := range runs {
		if r.SchemaVersion == historySchemaVersion {
			kept = append(kept, r)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Timestamp.After(kept[j].Timestamp) })
	return kept, nil
}

// Append stores a run and returns it along with the previous run recorded on
// the same network, if any.
func (h *HistoryStore) Append(r Run) (Run, *Run, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// A history we cannot parse is replaced rather than treated as a failure:
	// the run being appended is still valid and must not be lost because an
	// older file got damaged.
	runs, err := h.loadLocked()
	if err != nil {
		log.Printf("history: %v (starting a new file)", err)
	}

	var previous *Run
	for i := range runs {
		if runs[i].NetworkKey == r.NetworkKey && runs[i].ID != r.ID && !runs[i].Aborted {
			p := runs[i]
			previous = &p
			break
		}
	}

	runs = append([]Run{r}, runs...)
	if len(runs) > maxHistoryRuns {
		runs = runs[:maxHistoryRuns]
	}

	if err := h.writeLocked(runs); err != nil {
		return r, previous, err
	}
	return r, previous, nil
}

func (h *HistoryStore) writeLocked(runs []Run) error {
	if h.path == "" {
		return nil // history disabled
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		return err
	}
	tmp := h.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, h.path)
}

// newRunID returns a short random identifier for a run.
func newRunID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
