package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newRun(key string, p50 float64, ts time.Time) Run {
	return Run{
		SchemaVersion: historySchemaVersion,
		ID:            newRunID(),
		Timestamp:     ts,
		NetworkKey:    key,
		Download:      DirStats{Samples: 10, P50Mbps: p50},
		Upload:        DirStats{Samples: 10, P50Mbps: p50 / 2},
	}
}

func TestHistoryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	h := NewHistoryStore(path)

	runs, err := h.Load()
	if err != nil {
		t.Fatalf("loading a missing file must not error: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected no runs, got %d", len(runs))
	}

	first := newRun("ssid:Home", 400, time.Now().Add(-time.Hour))
	stored, previous, err := h.Append(first)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if previous != nil {
		t.Error("the first run on a network has no predecessor")
	}
	if stored.ID != first.ID {
		t.Errorf("stored run ID changed: %q vs %q", stored.ID, first.ID)
	}

	runs, err = h.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != first.ID {
		t.Fatalf("expected the run back, got %+v", runs)
	}
}

func TestHistoryComparesAgainstSameNetworkOnly(t *testing.T) {
	h := NewHistoryStore(filepath.Join(t.TempDir(), "history.json"))

	home := newRun("ssid:Home", 400, time.Now().Add(-2*time.Hour))
	if _, _, err := h.Append(home); err != nil {
		t.Fatalf("append: %v", err)
	}
	office := newRun("ssid:Office", 900, time.Now().Add(-time.Hour))
	if _, _, err := h.Append(office); err != nil {
		t.Fatalf("append: %v", err)
	}

	// A new run at home must be compared to the previous home run, not to the
	// faster office one.
	second := newRun("ssid:Home", 450, time.Now())
	_, previous, err := h.Append(second)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if previous == nil {
		t.Fatal("expected a previous run on the same network")
	}
	if previous.ID != home.ID {
		t.Errorf("compared against the wrong run: got %q, want %q", previous.ID, home.ID)
	}
	if previous.Download.P50Mbps != 400 {
		t.Errorf("previous download: got %v, want 400", previous.Download.P50Mbps)
	}
}

func TestHistorySkipsAbortedRunsWhenComparing(t *testing.T) {
	h := NewHistoryStore(filepath.Join(t.TempDir(), "history.json"))

	good := newRun("ssid:Home", 400, time.Now().Add(-2*time.Hour))
	if _, _, err := h.Append(good); err != nil {
		t.Fatalf("append: %v", err)
	}
	bad := newRun("ssid:Home", 12, time.Now().Add(-time.Hour))
	bad.Aborted = true
	if _, _, err := h.Append(bad); err != nil {
		t.Fatalf("append: %v", err)
	}

	_, previous, err := h.Append(newRun("ssid:Home", 410, time.Now()))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if previous == nil || previous.ID != good.ID {
		t.Errorf("an aborted run is not a valid baseline; got %+v", previous)
	}
}

func TestHistoryNewestFirst(t *testing.T) {
	h := NewHistoryStore(filepath.Join(t.TempDir(), "history.json"))
	base := time.Now().Add(-10 * time.Hour)
	for i := 0; i < 5; i++ {
		if _, _, err := h.Append(newRun("ssid:Home", float64(100*i), base.Add(time.Duration(i)*time.Hour))); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	runs, err := h.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(runs) != 5 {
		t.Fatalf("expected 5 runs, got %d", len(runs))
	}
	for i := 1; i < len(runs); i++ {
		if runs[i-1].Timestamp.Before(runs[i].Timestamp) {
			t.Fatalf("runs are not newest-first: %v then %v", runs[i-1].Timestamp, runs[i].Timestamp)
		}
	}
}

func TestHistoryCapsFileSize(t *testing.T) {
	if testing.Short() {
		t.Skip("writes maxHistoryRuns+ entries")
	}
	h := NewHistoryStore(filepath.Join(t.TempDir(), "history.json"))
	base := time.Now().Add(-time.Duration(maxHistoryRuns+10) * time.Minute)
	for i := 0; i < maxHistoryRuns+10; i++ {
		if _, _, err := h.Append(newRun("ssid:Home", 100, base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	runs, err := h.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(runs) != maxHistoryRuns {
		t.Errorf("expected the file capped at %d runs, got %d", maxHistoryRuns, len(runs))
	}
}

// A damaged history file must not stop the tool from running.
func TestHistoryToleratesCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := os.WriteFile(path, []byte("{not json at all"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	h := NewHistoryStore(path)

	runs, err := h.Load()
	if err == nil {
		t.Error("a corrupt file should be reported")
	}
	if runs == nil || len(runs) != 0 {
		t.Errorf("a corrupt file should read as empty, got %+v", runs)
	}

	// And a new run can still be recorded over the top of it.
	if _, _, err := h.Append(newRun("ssid:Home", 100, time.Now())); err != nil {
		t.Fatalf("append after corruption: %v", err)
	}
	runs, err = h.Load()
	if err != nil {
		t.Fatalf("load after repair: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("expected 1 run after recovery, got %d", len(runs))
	}
}

func TestHistoryIgnoresUnknownSchemaVersions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	future := []map[string]any{
		{"schema_version": 99, "id": "future"},
		{"schema_version": historySchemaVersion, "id": "current", "timestamp": time.Now()},
	}
	data, err := json.Marshal(future)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	runs, err := NewHistoryStore(path).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "current" {
		t.Errorf("runs from an unknown schema must be skipped, got %+v", runs)
	}
}

func TestHistoryDisabledWhenPathEmpty(t *testing.T) {
	h := NewHistoryStore("")
	if _, _, err := h.Append(newRun("ssid:Home", 100, time.Now())); err != nil {
		t.Errorf("an empty path should disable persistence quietly, got %v", err)
	}
}

func TestNewRunIDIsUnique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := newRunID()
		if id == "" {
			t.Fatal("empty run ID")
		}
		if seen[id] {
			t.Fatalf("duplicate run ID %q", id)
		}
		seen[id] = true
	}
}
