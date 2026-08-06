package main

import (
	"strings"
	"testing"
)

func TestFormatRunHeader(t *testing.T) {
	peer := PeerInfo{Label: "Android / Chrome", Addr: "192.168.1.23"}
	rows := []streamStat{
		// Deliberately out of order: the header must sort them.
		{Index: 2, From: "192.168.1.23:54314", To: "192.168.1.10:8080"},
		{Index: 0, From: "192.168.1.23:54312", To: "192.168.1.10:8080"},
		{Index: 1, From: "192.168.1.23:54313", To: "192.168.1.10:8080"},
	}

	got := formatRunHeader(peer, "3568a35a", ModeManual, DirectionDownload, 64*1024, rows)

	for _, want := range []string{
		"Test pornit", "Android / Chrome", "192.168.1.23", "3568a35a",
		"manual, până la Stop", "doar download", "3 streamuri", "64 KiB",
		"#0  192.168.1.23:54312 → 192.168.1.10:8080",
		"#1  192.168.1.23:54313",
		"#2  192.168.1.23:54314",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("header missing %q:\n%s", want, got)
		}
	}

	// Sorted, not in insertion order.
	if strings.Index(got, "#0") > strings.Index(got, "#1") {
		t.Errorf("streams are not sorted by index:\n%s", got)
	}
}

func TestFormatRunHeaderNoStreams(t *testing.T) {
	got := formatRunHeader(PeerInfo{Label: "X"}, "s1", ModeAuto, DirectionBoth, 65536, nil)
	if !strings.Contains(got, "niciunul") {
		t.Errorf("a session with no streams should say so:\n%s", got)
	}
}

func TestFormatStreamTable(t *testing.T) {
	rows := []streamStat{
		{Index: 0, Bytes: 300 << 20, Frames: 4800, ElapsedMs: 10000, Stalls: 0},
		{Index: 1, Bytes: 300 << 20, Frames: 4800, ElapsedMs: 10000, Stalls: 2},
		{Index: 2, Bytes: 300 << 20, Frames: 4800, ElapsedMs: 10000, Stalls: 0},
		{Index: 3, Bytes: 300 << 20, Frames: 4800, ElapsedMs: 10000, Stalls: 1},
	}
	got := formatStreamTable("Download încheiat", rows, true)

	if !strings.Contains(got, "Blocaje") {
		t.Error("the stall column was requested but is missing")
	}
	// Four equal streams: 25% each, and the total is four times one stream.
	if strings.Count(got, "25.0%") != 4 {
		t.Errorf("each of four equal streams should read 25.0%%:\n%s", got)
	}
	if !strings.Contains(got, "1.2 GiB") {
		t.Errorf("total should be the sum of the four:\n%s", got)
	}
	if !strings.Contains(got, "tot") {
		t.Error("missing the total row")
	}
	// An even split is normal and must not be flagged.
	if strings.Contains(got, "din trafic") {
		t.Errorf("an even split must not raise a note:\n%s", got)
	}
}

func TestFormatStreamTableHidesStallsForUpload(t *testing.T) {
	rows := []streamStat{{Index: 0, Bytes: 1 << 20, Frames: 16, ElapsedMs: 1000}}
	got := formatStreamTable("Upload încheiat", rows, false)
	if strings.Contains(got, "Blocaje") {
		t.Errorf("upload has no server-side writes to stall on:\n%s", got)
	}
}

// The whole point of the breakdown: a starved stream is invisible in the total.
func TestFormatStreamTableFlagsStarvedStream(t *testing.T) {
	rows := []streamStat{
		{Index: 0, Bytes: 900 << 20, Frames: 14400, ElapsedMs: 10000},
		{Index: 1, Bytes: 300 << 20, Frames: 4800, ElapsedMs: 10000},
		{Index: 2, Bytes: 280 << 20, Frames: 4480, ElapsedMs: 10000},
		{Index: 3, Bytes: 10 << 20, Frames: 160, ElapsedMs: 10000},
	}
	got := formatStreamTable("Download încheiat", rows, true)
	if !strings.Contains(got, "stream #3") {
		t.Errorf("the starved stream should be named:\n%s", got)
	}
	if !strings.Contains(got, "din trafic") {
		t.Errorf("an uneven split should raise a note:\n%s", got)
	}
}

func TestFormatStreamTableSingleStreamHasNoBalanceNote(t *testing.T) {
	rows := []streamStat{{Index: 0, Bytes: 1 << 30, Frames: 16384, ElapsedMs: 10000}}
	got := formatStreamTable("Download încheiat", rows, true)
	if strings.Contains(got, "din trafic") {
		t.Errorf("one stream is always 100%%; nothing to balance:\n%s", got)
	}
}

func TestFormatStreamTableEmpty(t *testing.T) {
	got := formatStreamTable("Download încheiat", nil, true)
	if !strings.Contains(got, "niciun stream") {
		t.Errorf("expected an explicit empty message:\n%s", got)
	}
}

func TestStreamStatMbps(t *testing.T) {
	// 125 MB in 1 s is 1000 Mbps.
	s := streamStat{Bytes: 125_000_000, ElapsedMs: 1000}
	almost(t, s.Mbps(), 1000, "stream Mbps")

	if got := (streamStat{Bytes: 100, ElapsedMs: 0}).Mbps(); got != 0 {
		t.Errorf("a stream that never ran has no rate, got %v", got)
	}
}

func TestBalanceNoteThresholds(t *testing.T) {
	even := []streamStat{
		{Index: 0, Bytes: 100}, {Index: 1, Bytes: 100},
		{Index: 2, Bytes: 100}, {Index: 3, Bytes: 100},
	}
	if note := balanceNote(even, 400); note != "" {
		t.Errorf("perfectly even split flagged: %q", note)
	}

	// Ordinary TCP variation, roughly ±20%, must stay quiet.
	normal := []streamStat{
		{Index: 0, Bytes: 120}, {Index: 1, Bytes: 95},
		{Index: 2, Bytes: 105}, {Index: 3, Bytes: 80},
	}
	if note := balanceNote(normal, 400); note != "" {
		t.Errorf("normal variation flagged: %q", note)
	}

	starved := []streamStat{
		{Index: 0, Bytes: 190}, {Index: 1, Bytes: 190},
		{Index: 2, Bytes: 190}, {Index: 3, Bytes: 10},
	}
	if note := balanceNote(starved, 580); !strings.Contains(note, "#3") {
		t.Errorf("starved stream not flagged: %q", note)
	}
}

func TestFormatThousands(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1 000"},
		{48569, "48 569"},
		{1234567, "1 234 567"},
	}
	for _, tc := range tests {
		if got := formatThousands(tc.in); got != tc.want {
			t.Errorf("formatThousands(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRuleCarriesAClock(t *testing.T) {
	got := rule("Test pornit")
	if !strings.Contains(got, "Test pornit") {
		t.Errorf("title missing: %q", got)
	}
	if !strings.HasPrefix(got, "\n") {
		t.Error("a rule should start on a fresh line, to separate runs")
	}
	if strings.Count(got, ":") != 2 {
		t.Errorf("expected an HH:MM:SS clock in %q", got)
	}
}
