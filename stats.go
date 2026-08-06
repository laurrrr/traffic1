package main

import (
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	// WarmupMs is discarded from the head of every direction before percentiles
	// are computed. TCP slow-start plus Wi-Fi rate adaptation make the first
	// second unrepresentative of steady state; including it drags p50 down and
	// makes runs incomparable. Every export records that this happened.
	WarmupMs int64 = 1000

	// schedulingLagNoiseMs is how much client-side timer lateness counts as
	// noise rather than as a threat to the latency figures.
	schedulingLagNoiseMs = 5.0

	// minAnsweredRatio is how many of the pings issued during a load phase must
	// eventually come back for its latency figure to mean anything.
	minAnsweredRatio = 0.5

	// maxPlausibleBufferBytes bounds how much data a LAN path can plausibly hold
	// in flight. Consumer routers and access points buffer megabytes — a few
	// tens at the very worst. Latency under load is queueing delay, so the
	// measured increase implies a buffer of delta x throughput; when that works
	// out to more than this, the queue is not in the network. It is inside the
	// measuring device, whose own message backlog is indistinguishable from
	// network delay at the point where the round trip is timed.
	maxPlausibleBufferBytes = 256 << 20

	// SampleWindow is the width of one throughput sample. Samples are only
	// emitted for windows that fully elapsed, so a partial tail never inflates
	// or deflates a reading.
	SampleWindow = 250 * time.Millisecond
)

// Sample is one throughput window. Window is the index since the start of the
// phase, which is what makes samples from parallel streams addable.
type Sample struct {
	Window int     `json:"window"`
	AtMs   int64   `json:"at_ms"`
	Bytes  int64   `json:"bytes"`
	Mbps   float64 `json:"mbps"`
}

// DirStats summarises one direction of a run.
type DirStats struct {
	Samples    int     `json:"samples"`
	TotalBytes int64   `json:"total_bytes"`
	MinMbps    float64 `json:"min_mbps"`
	P50Mbps    float64 `json:"p50_mbps"`
	P95Mbps    float64 `json:"p95_mbps"`
	MaxMbps    float64 `json:"max_mbps"`
	MeanMbps   float64 `json:"mean_mbps"`
	WarmupMs   int64   `json:"warmup_discarded_ms"`
	Streams    int     `json:"streams"`
}

// LatencyStats summarises a set of round trips.
type LatencyStats struct {
	Count int `json:"count"`
	// Sent is how many pings were issued in this phase. Count can be lower:
	// under heavy load a reply may not arrive before the phase ends. The gap is
	// the signal that latency could not be observed at all.
	Sent     int     `json:"sent"`
	MinMs    float64 `json:"min_ms"`
	P50Ms    float64 `json:"p50_ms"`
	P95Ms    float64 `json:"p95_ms"`
	JitterMs float64 `json:"jitter_ms"`
}

// AnsweredRatio is the share of pings that came back inside their own phase.
// Phases that issued no pings count as fully answered, since there is nothing
// to be suspicious about.
func (s LatencyStats) AnsweredRatio() float64 {
	if s.Sent <= 0 {
		return 1
	}
	r := float64(s.Count) / float64(s.Sent)
	if r > 1 {
		return 1
	}
	return r
}

// LatencyReport holds latency measured while the link was idle and while it
// was saturated in each direction. The gap between them is the headline number.
type LatencyReport struct {
	Idle           LatencyStats `json:"idle"`
	LoadedDownload LatencyStats `json:"loaded_download"`
	LoadedUpload   LatencyStats `json:"loaded_upload"`

	// Scheduling is how late the client's own timers fired during the load
	// phases. A browser dispatches every WebSocket message on one thread, so a
	// saturated link can leave the reply to a ping queued behind megabytes of
	// test data. That delay is indistinguishable from network latency at the
	// point of measurement, so it is measured separately and reported.
	Scheduling LatencyStats `json:"scheduling_lag"`
}

// Bufferbloat is the difference between idle latency and latency under load.
type Bufferbloat struct {
	IdleP50Ms   float64 `json:"idle_p50_ms"`
	LoadedP95Ms float64 `json:"loaded_p95_ms"`
	DeltaMs     float64 `json:"delta_ms"`
	Grade       string  `json:"grade"`
	Label       string  `json:"label"`

	// SchedulingLagMs is how much of the measured increase could have come from
	// the client being unable to run its own code, rather than from the network.
	SchedulingLagMs float64 `json:"scheduling_lag_p95_ms"`
	// AnsweredRatio is the worst share of pings that came back at all.
	AnsweredRatio float64 `json:"answered_ratio"`
	// ImpliedBufferBytes is how much buffering the measured increase would
	// require at the measured throughput. An implausible figure here is the
	// clearest sign the delay was queued in the client rather than the path.
	ImpliedBufferBytes int64 `json:"implied_buffer_bytes"`
	// Trustworthy is false when the measured increase describes the measuring
	// device rather than the link.
	Trustworthy bool `json:"trustworthy"`
}

// Mbps converts a byte count observed over d into megabits per second.
func Mbps(bytes int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(bytes) * 8 / d.Seconds() / 1e6
}

// Percentile returns the linearly interpolated p-th percentile of values.
// Percentiles, never bare means: one stalled window ruins an average.
func Percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	if p <= 0 {
		return s[0]
	}
	if p >= 100 {
		return s[len(s)-1]
	}
	k := (p / 100) * float64(len(s)-1)
	lo := int(math.Floor(k))
	hi := int(math.Ceil(k))
	if lo == hi {
		return s[lo]
	}
	return s[lo] + (s[hi]-s[lo])*(k-float64(lo))
}

// AggregateSamples merges per-stream series into one aggregate series.
//
// Parallel TCP streams have to be summed *within the same time window*:
// averaging them under-reports the link by a factor of the stream count, and
// concatenating them counts the same wall-clock second several times over.
// Window indices are assigned from a start time shared by every stream in the
// session, which is what makes the sum meaningful.
func AggregateSamples(perStream [][]Sample) []Sample {
	byWindow := make(map[int]int64)
	for _, series := range perStream {
		for _, s := range series {
			byWindow[s.Window] += s.Bytes
		}
	}
	if len(byWindow) == 0 {
		return []Sample{}
	}
	windows := make([]int, 0, len(byWindow))
	for w := range byWindow {
		windows = append(windows, w)
	}
	sort.Ints(windows)

	out := make([]Sample, 0, len(windows))
	for _, w := range windows {
		b := byWindow[w]
		out = append(out, Sample{
			Window: w,
			AtMs:   int64(w+1) * SampleWindow.Milliseconds(),
			Bytes:  b,
			Mbps:   Mbps(b, SampleWindow),
		})
	}
	return out
}

// TrimWarmup drops every sample whose window closed before warmupMs elapsed.
func TrimWarmup(samples []Sample, warmupMs int64) []Sample {
	out := make([]Sample, 0, len(samples))
	for _, s := range samples {
		if s.AtMs > warmupMs {
			out = append(out, s)
		}
	}
	return out
}

// TrimTail drops the final window of an aggregate. Streams stop within a few
// milliseconds of each other, so the last window is usually short a stream or
// two and reads as a throughput collapse that never happened.
func TrimTail(samples []Sample) []Sample {
	if len(samples) <= 2 {
		return samples
	}
	return samples[:len(samples)-1]
}

// Summarize reduces a sample series to the numbers that get reported. The
// caller is expected to have trimmed warmup already; WarmupMs is recorded so
// the export can say so.
func Summarize(samples []Sample, streams int, warmupMs int64) DirStats {
	st := DirStats{
		Samples:  len(samples),
		WarmupMs: warmupMs,
		Streams:  streams,
	}
	if len(samples) == 0 {
		return st
	}
	vals := make([]float64, 0, len(samples))
	var sum float64
	for _, s := range samples {
		vals = append(vals, s.Mbps)
		sum += s.Mbps
		st.TotalBytes += s.Bytes
	}
	st.MinMbps = Percentile(vals, 0)
	st.P50Mbps = Percentile(vals, 50)
	st.P95Mbps = Percentile(vals, 95)
	st.MaxMbps = Percentile(vals, 100)
	st.MeanMbps = sum / float64(len(vals))
	return st
}

// Jitter is the mean absolute difference between consecutive round trips,
// the RFC 3550 style estimate rather than a standard deviation.
func Jitter(rtts []float64) float64 {
	if len(rtts) < 2 {
		return 0
	}
	var sum float64
	for i := 1; i < len(rtts); i++ {
		sum += math.Abs(rtts[i] - rtts[i-1])
	}
	return sum / float64(len(rtts)-1)
}

// SummarizeLatency reduces round trip times to reportable numbers.
func SummarizeLatency(rtts []float64) LatencyStats {
	st := LatencyStats{Count: len(rtts)}
	if len(rtts) == 0 {
		return st
	}
	st.MinMs = Percentile(rtts, 0)
	st.P50Ms = Percentile(rtts, 50)
	st.P95Ms = Percentile(rtts, 95)
	st.JitterMs = Jitter(rtts)
	return st
}

// GradeBufferbloat maps added latency under load to a grade and a plain-language
// label. Thresholds follow the widely used A–F bufferbloat scale.
func GradeBufferbloat(deltaMs float64) (grade, label string) {
	switch {
	case deltaMs < 5:
		return "A", "fără bufferbloat"
	case deltaMs < 30:
		return "B", "bufferbloat ușor"
	case deltaMs < 100:
		return "C", "bufferbloat moderat"
	case deltaMs < 300:
		return "D", "bufferbloat sever"
	default:
		return "F", "bufferbloat foarte sever"
	}
}

// ComputeBufferbloat compares idle median latency against the worst case seen
// while the link was saturated in either direction. peakMbps is the throughput
// that load was running at, used to sanity-check the result against physics.
func ComputeBufferbloat(r LatencyReport, peakMbps float64) Bufferbloat {
	loaded := math.Max(r.LoadedDownload.P95Ms, r.LoadedUpload.P95Ms)
	bb := Bufferbloat{
		IdleP50Ms:   r.Idle.P50Ms,
		LoadedP95Ms: loaded,
	}
	if r.Idle.Count == 0 || (r.LoadedDownload.Count == 0 && r.LoadedUpload.Count == 0) {
		bb.Grade = "?"
		bb.Label = "latență sub sarcină nemăsurată"
		return bb
	}
	bb.DeltaMs = math.Max(0, loaded-r.Idle.P50Ms)
	bb.Grade, bb.Label = GradeBufferbloat(bb.DeltaMs)

	// Two ways the measurement can describe the device instead of the link.
	//
	// One: the client could not run its own code, so it timed its own delay.
	// Below a few milliseconds that is noise; once it accounts for half the
	// increase, the grade is about the browser.
	bb.SchedulingLagMs = r.Scheduling.P95Ms
	lagOK := bb.SchedulingLagMs < schedulingLagNoiseMs || bb.DeltaMs > 2*bb.SchedulingLagMs

	// Two: most pings issued during the load never came back while it lasted.
	// Whatever did come back is, by construction, the slowest tail — and on a
	// link fast enough to saturate the client's own network stack, that says
	// more about the stack than about buffering in the path.
	bb.AnsweredRatio = math.Min(r.LoadedDownload.AnsweredRatio(), r.LoadedUpload.AnsweredRatio())
	answeredOK := bb.AnsweredRatio >= minAnsweredRatio

	// Three: the increase implies more buffering than the path could hold.
	// Queueing delay is buffer divided by rate, so rate times delay gives the
	// buffer it would have taken. This is the check that catches a client whose
	// own message queue ran seconds deep while its timers still fired on time —
	// nothing on the client side looks wrong, but the physics does not close.
	plausibleOK := true
	if bb.DeltaMs > 0 && peakMbps > 0 {
		bb.ImpliedBufferBytes = int64(bb.DeltaMs / 1000 * peakMbps * 1e6 / 8)
		plausibleOK = bb.ImpliedBufferBytes <= maxPlausibleBufferBytes
	}

	bb.Trustworthy = lagOK && answeredOK && plausibleOK
	return bb
}

// BuildVerdict turns the numbers into a sentence a human can act on. This lives
// in Go rather than the frontend so the phone, the desktop window and the
// history file never disagree about what a run means.
func BuildVerdict(down, up DirStats, bb Bufferbloat, aborted bool) string {
	if aborted {
		return "Rulare întreruptă — cifrele de mai jos sunt parțiale și nu trebuie folosite pentru comparații."
	}
	if down.Samples == 0 && up.Samples == 0 {
		return "Nicio măsurătoare validă: rulare prea scurtă sau conexiune pierdută."
	}

	speed := fmt.Sprintf("Rețeaua ta livrează %s la download și %s la upload.",
		FormatMbps(down.P50Mbps), FormatMbps(up.P50Mbps))

	if bb.Grade == "?" {
		return speed + " Latența sub sarcină nu a putut fi măsurată."
	}
	if !bb.Trustworthy {
		reason := fmt.Sprintf("browserul a fost blocat până la %s în timpul testului",
			FormatMs(bb.SchedulingLagMs))
		if bb.AnsweredRatio < minAnsweredRatio {
			reason = fmt.Sprintf("doar %.0f%% dintre pachetele de test au primit răspuns cât a durat încărcarea",
				bb.AnsweredRatio*100)
		} else if bb.ImpliedBufferBytes > maxPlausibleBufferBytes {
			reason = fmt.Sprintf("asta ar cere %s de tampon în rețea, ceea ce nu există",
				FormatBytes(bb.ImpliedBufferBytes))
		}
		return fmt.Sprintf(
			"%s Latența sub sarcină a ieșit %s, dar %s, deci cifra descrie dispozitivul de măsurare, "+
				"nu rețeaua. Repetă de pe un telefon, pe Wi-Fi, sau cu mai puține streamuri.",
			speed, FormatMs(bb.LoadedP95Ms), reason)
	}
	return fmt.Sprintf("%s Latența crește de la %s la %s sub sarcină — %s.",
		speed, FormatMs(bb.IdleP50Ms), FormatMs(bb.LoadedP95Ms), bb.Label)
}

// FormatMbps prints a throughput figure with a sensible number of digits.
func FormatMbps(v float64) string {
	switch {
	case v >= 100:
		return fmt.Sprintf("%.0f Mbps", v)
	case v >= 10:
		return fmt.Sprintf("%.1f Mbps", v)
	default:
		return fmt.Sprintf("%.2f Mbps", v)
	}
}

// FormatBytes prints a byte count in the largest unit that keeps it readable.
func FormatBytes(n int64) string {
	f := float64(n)
	switch {
	case f >= 1<<30:
		return fmt.Sprintf("%.1f GB", f/(1<<30))
	case f >= 1<<20:
		return fmt.Sprintf("%.0f MB", f/(1<<20))
	case f >= 1<<10:
		return fmt.Sprintf("%.0f kB", f/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// FormatMs prints a latency figure with a sensible number of digits.
func FormatMs(v float64) string {
	if v >= 10 {
		return fmt.Sprintf("%.0f ms", v)
	}
	return fmt.Sprintf("%.1f ms", v)
}

// DeltaPct returns how far measured is from reference, as a percentage of
// reference. Used to compare the client's byte counter against the server's.
func DeltaPct(measured, reference int64) float64 {
	if reference == 0 {
		return 0
	}
	return (float64(measured) - float64(reference)) / float64(reference) * 100
}

// clampDuration bounds a client-supplied test duration. Zero or negative means
// "use the default"; anything above maxDuration is capped so a malformed or
// hostile client cannot pin a stream open indefinitely.
func clampDuration(ms int) time.Duration {
	d := time.Duration(ms) * time.Millisecond
	if d <= 0 {
		return defaultDuration
	}
	if d > maxDuration {
		return maxDuration
	}
	if d < minDuration {
		return minDuration
	}
	return d
}
