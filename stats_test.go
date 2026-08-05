package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

const eps = 1e-9

func almost(t *testing.T, got, want float64, msg string) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s: got %v, want %v", msg, got, want)
	}
}

func TestPercentile(t *testing.T) {
	tests := []struct {
		name   string
		values []float64
		p      float64
		want   float64
	}{
		{"empty", nil, 50, 0},
		{"single", []float64{7}, 50, 7},
		{"single p95", []float64{7}, 95, 7},
		{"odd median", []float64{1, 2, 3, 4, 5}, 50, 3},
		{"even median interpolates", []float64{1, 2, 3, 4}, 50, 2.5},
		{"min", []float64{5, 1, 9}, 0, 1},
		{"max", []float64{5, 1, 9}, 100, 9},
		{"unsorted input", []float64{5, 1, 3, 2, 4}, 50, 3},
		// k = 0.95*(10-1) = 8.55 -> between s[8]=9 and s[9]=10
		{"p95 interpolated", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 95, 9.55},
		{"below range clamps to min", []float64{2, 4}, -10, 2},
		{"above range clamps to max", []float64{2, 4}, 200, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			almost(t, Percentile(tc.values, tc.p), tc.want, "Percentile")
		})
	}
}

func TestPercentileDoesNotMutateInput(t *testing.T) {
	in := []float64{5, 1, 3}
	Percentile(in, 50)
	if in[0] != 5 || in[1] != 1 || in[2] != 3 {
		t.Fatalf("Percentile mutated its input: %v", in)
	}
}

func TestMbps(t *testing.T) {
	// 1 MB in 250 ms = 8 Mbit in 0.25 s = 33.554432 Mbps
	almost(t, Mbps(1024*1024, 250*time.Millisecond), 33.554432, "Mbps")
	almost(t, Mbps(1_000_000, time.Second), 8, "Mbps 1MB/s")
	if got := Mbps(1000, 0); got != 0 {
		t.Errorf("zero duration must yield 0, got %v", got)
	}
	if got := Mbps(1000, -time.Second); got != 0 {
		t.Errorf("negative duration must yield 0, got %v", got)
	}
}

// The central correctness property of parallel streams: concurrent streams are
// summed inside a window. Averaging them would under-report the link by the
// stream count, which is exactly the bug a naive implementation ships with.
func TestAggregateSamplesSumsConcurrentStreams(t *testing.T) {
	const oneMB = 1024 * 1024
	perStream := make([][]Sample, 4)
	for i := range perStream {
		perStream[i] = []Sample{{
			Window: 0, AtMs: 250, Bytes: oneMB, Mbps: Mbps(oneMB, SampleWindow),
		}}
	}

	agg := AggregateSamples(perStream)
	if len(agg) != 1 {
		t.Fatalf("expected 1 aggregate window, got %d", len(agg))
	}
	if agg[0].Bytes != 4*oneMB {
		t.Errorf("bytes: got %d, want %d", agg[0].Bytes, 4*oneMB)
	}
	almost(t, agg[0].Mbps, Mbps(4*oneMB, SampleWindow), "aggregate Mbps")
	if agg[0].Window != 0 {
		t.Errorf("window: got %d, want 0", agg[0].Window)
	}
}

func TestAggregateSamplesAlignsWindowsAcrossStreams(t *testing.T) {
	// Stream A runs windows 0..2, stream B only 1..2 (it started late) and
	// stream C only window 0 (it finished early). Every window must appear once,
	// in order, holding the sum of whatever was actually delivered in it.
	perStream := [][]Sample{
		{{Window: 0, Bytes: 100}, {Window: 1, Bytes: 200}, {Window: 2, Bytes: 300}},
		{{Window: 1, Bytes: 20}, {Window: 2, Bytes: 30}},
		{{Window: 0, Bytes: 1}},
	}
	agg := AggregateSamples(perStream)
	if len(agg) != 3 {
		t.Fatalf("expected 3 windows, got %d", len(agg))
	}
	want := []int64{101, 220, 330}
	for i, w := range want {
		if agg[i].Window != i {
			t.Errorf("window %d out of order: got index %d", i, agg[i].Window)
		}
		if agg[i].Bytes != w {
			t.Errorf("window %d bytes: got %d, want %d", i, agg[i].Bytes, w)
		}
	}
	// AtMs must be the end of the window, so warmup trimming is comparable
	// across runs regardless of stream count.
	if agg[0].AtMs != 250 || agg[2].AtMs != 750 {
		t.Errorf("AtMs wrong: %d, %d", agg[0].AtMs, agg[2].AtMs)
	}
}

func TestAggregateSamplesEmpty(t *testing.T) {
	if got := AggregateSamples(nil); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
	if got := AggregateSamples([][]Sample{{}, {}}); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestTrimWarmup(t *testing.T) {
	samples := []Sample{
		{Window: 0, AtMs: 250}, {Window: 1, AtMs: 500}, {Window: 2, AtMs: 750},
		{Window: 3, AtMs: 1000}, {Window: 4, AtMs: 1250}, {Window: 5, AtMs: 1500},
	}
	got := TrimWarmup(samples, WarmupMs)
	if len(got) != 2 {
		t.Fatalf("expected 2 samples after trimming 1000 ms, got %d", len(got))
	}
	if got[0].AtMs != 1250 || got[1].AtMs != 1500 {
		t.Errorf("wrong samples kept: %+v", got)
	}
}

func TestTrimWarmupDropsEverythingWhenRunTooShort(t *testing.T) {
	samples := []Sample{{AtMs: 250}, {AtMs: 500}}
	if got := TrimWarmup(samples, WarmupMs); len(got) != 0 {
		t.Errorf("expected no samples, got %d", len(got))
	}
}

func TestTrimTail(t *testing.T) {
	five := []Sample{{Window: 0}, {Window: 1}, {Window: 2}, {Window: 3}, {Window: 4}}
	if got := TrimTail(five); len(got) != 4 {
		t.Errorf("expected 4, got %d", len(got))
	}
	// Too few samples to afford dropping one.
	two := []Sample{{Window: 0}, {Window: 1}}
	if got := TrimTail(two); len(got) != 2 {
		t.Errorf("expected 2 kept, got %d", len(got))
	}
}

func TestSummarize(t *testing.T) {
	samples := []Sample{
		{Bytes: 100, Mbps: 10},
		{Bytes: 100, Mbps: 20},
		{Bytes: 100, Mbps: 30},
		{Bytes: 100, Mbps: 40},
	}
	st := Summarize(samples, 4, WarmupMs)
	if st.Samples != 4 {
		t.Errorf("samples: got %d, want 4", st.Samples)
	}
	if st.TotalBytes != 400 {
		t.Errorf("total bytes: got %d, want 400", st.TotalBytes)
	}
	if st.Streams != 4 {
		t.Errorf("streams: got %d, want 4", st.Streams)
	}
	if st.WarmupMs != WarmupMs {
		t.Errorf("warmup not recorded: got %d", st.WarmupMs)
	}
	almost(t, st.MinMbps, 10, "min")
	almost(t, st.MaxMbps, 40, "max")
	almost(t, st.MeanMbps, 25, "mean")
	almost(t, st.P50Mbps, 25, "p50")
}

func TestSummarizeEmpty(t *testing.T) {
	st := Summarize(nil, 4, WarmupMs)
	if st.Samples != 0 || st.P50Mbps != 0 || st.TotalBytes != 0 {
		t.Errorf("expected zero stats, got %+v", st)
	}
	if st.WarmupMs != WarmupMs {
		t.Errorf("warmup must still be recorded, got %d", st.WarmupMs)
	}
}

func TestJitter(t *testing.T) {
	// |12-10| + |11-12| + |13-11| = 2 + 1 + 2 = 5, over 3 intervals
	almost(t, Jitter([]float64{10, 12, 11, 13}), 5.0/3.0, "jitter")
	almost(t, Jitter([]float64{5, 5, 5}), 0, "constant RTT has no jitter")
	almost(t, Jitter([]float64{5}), 0, "single sample")
	almost(t, Jitter(nil), 0, "empty")
}

func TestSummarizeLatency(t *testing.T) {
	st := SummarizeLatency([]float64{10, 20, 30, 40, 50})
	if st.Count != 5 {
		t.Errorf("count: got %d", st.Count)
	}
	almost(t, st.MinMs, 10, "min")
	almost(t, st.P50Ms, 30, "p50")
	almost(t, st.JitterMs, 10, "jitter")
}

func TestGradeBufferbloat(t *testing.T) {
	tests := []struct {
		delta float64
		grade string
	}{
		{0, "A"}, {4.99, "A"},
		{5, "B"}, {29.9, "B"},
		{30, "C"}, {99.9, "C"},
		{100, "D"}, {299.9, "D"},
		{300, "F"}, {5000, "F"},
	}
	for _, tc := range tests {
		got, label := GradeBufferbloat(tc.delta)
		if got != tc.grade {
			t.Errorf("delta %v: got grade %q, want %q", tc.delta, got, tc.grade)
		}
		if label == "" {
			t.Errorf("delta %v: empty label", tc.delta)
		}
	}
}

func TestComputeBufferbloat(t *testing.T) {
	r := LatencyReport{
		Idle:           LatencyStats{Count: 50, P50Ms: 3},
		LoadedDownload: LatencyStats{Count: 100, P95Ms: 45},
		LoadedUpload:   LatencyStats{Count: 100, P95Ms: 20},
	}
	bb := ComputeBufferbloat(r)
	almost(t, bb.IdleP50Ms, 3, "idle")
	almost(t, bb.LoadedP95Ms, 45, "worst loaded direction wins")
	almost(t, bb.DeltaMs, 42, "delta")
	if bb.Grade != "C" {
		t.Errorf("grade: got %q, want C", bb.Grade)
	}
}

func TestComputeBufferbloatWithoutLoadedSamples(t *testing.T) {
	bb := ComputeBufferbloat(LatencyReport{Idle: LatencyStats{Count: 50, P50Ms: 3}})
	if bb.Grade != "?" {
		t.Errorf("expected unknown grade, got %q", bb.Grade)
	}
}

func TestComputeBufferbloatNeverNegative(t *testing.T) {
	// Loaded latency below idle latency is measurement noise, not a negative
	// delta; reporting "-2 ms of bufferbloat" would be nonsense.
	bb := ComputeBufferbloat(LatencyReport{
		Idle:           LatencyStats{Count: 50, P50Ms: 10},
		LoadedDownload: LatencyStats{Count: 50, P95Ms: 8},
	})
	if bb.DeltaMs < 0 {
		t.Errorf("delta must clamp at 0, got %v", bb.DeltaMs)
	}
	if bb.Grade != "A" {
		t.Errorf("grade: got %q, want A", bb.Grade)
	}
}

func TestBuildVerdict(t *testing.T) {
	down := DirStats{Samples: 30, P50Mbps: 480}
	up := DirStats{Samples: 30, P50Mbps: 310}
	bb := Bufferbloat{IdleP50Ms: 3, LoadedP95Ms: 45, DeltaMs: 42, Grade: "C", Label: "bufferbloat moderat"}

	v := BuildVerdict(down, up, bb, false)
	for _, want := range []string{"480 Mbps", "310 Mbps", "3.0 ms", "45 ms", "bufferbloat moderat"} {
		if !strings.Contains(v, want) {
			t.Errorf("verdict %q missing %q", v, want)
		}
	}
}

func TestBuildVerdictAborted(t *testing.T) {
	v := BuildVerdict(DirStats{Samples: 5}, DirStats{Samples: 5}, Bufferbloat{Grade: "A"}, true)
	if !strings.Contains(v, "întreruptă") {
		t.Errorf("aborted run must say so, got %q", v)
	}
}

func TestBuildVerdictNoData(t *testing.T) {
	v := BuildVerdict(DirStats{}, DirStats{}, Bufferbloat{Grade: "?"}, false)
	if !strings.Contains(v, "Nicio măsurătoare") {
		t.Errorf("expected no-data verdict, got %q", v)
	}
}

func TestClampDuration(t *testing.T) {
	tests := []struct {
		name string
		ms   int
		want time.Duration
	}{
		{"zero uses default", 0, defaultDuration},
		{"negative uses default", -5000, defaultDuration},
		{"below minimum is raised", 10, minDuration},
		{"normal passes through", 10000, 10 * time.Second},
		{"at maximum", 60000, maxDuration},
		{"above maximum is capped", 999999, maxDuration},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampDuration(tc.ms); got != tc.want {
				t.Errorf("clampDuration(%d) = %v, want %v", tc.ms, got, tc.want)
			}
		})
	}
}

func TestDeltaPct(t *testing.T) {
	almost(t, DeltaPct(95, 100), -5, "client 5%% short")
	almost(t, DeltaPct(105, 100), 5, "client 5%% over")
	almost(t, DeltaPct(100, 100), 0, "equal")
	if got := DeltaPct(100, 0); got != 0 {
		t.Errorf("zero reference must yield 0, got %v", got)
	}
}

func TestFormatters(t *testing.T) {
	tests := []struct{ in float64; want string }{
		{480.4, "480 Mbps"},
		{48.44, "48.4 Mbps"},
		{4.444, "4.44 Mbps"},
	}
	for _, tc := range tests {
		if got := FormatMbps(tc.in); got != tc.want {
			t.Errorf("FormatMbps(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := FormatMs(45.6); got != "46 ms" {
		t.Errorf("FormatMs(45.6) = %q", got)
	}
	if got := FormatMs(3.04); got != "3.0 ms" {
		t.Errorf("FormatMs(3.04) = %q", got)
	}
}
