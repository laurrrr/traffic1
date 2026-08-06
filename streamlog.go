package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// streamStat is one data connection's share of a phase.
//
// The per-stream breakdown exists because the aggregate hides the failure that
// matters most with parallel streams: one connection taking most of the link
// while another is starved. Four streams at 25% each and one stream at 90% with
// three at 3% produce the same total, and only the first is a healthy link.
type streamStat struct {
	Index int
	// From is the client end of the connection and To the server end. Named by
	// direction rather than by "local"/"remote", which flip meaning depending
	// on which side is asking.
	From      string
	To        string
	Bytes     int64
	Frames    int64
	ElapsedMs int64
	Stalls    int
}

// Mbps is the mean rate this stream sustained over its own elapsed time.
func (s streamStat) Mbps() float64 {
	if s.ElapsedMs <= 0 {
		return 0
	}
	return Mbps(s.Bytes, time.Duration(s.ElapsedMs)*time.Millisecond)
}

// formatRunHeader renders the block printed when a run's first load phase
// starts, which is the moment every stream is known to be connected.
func formatRunHeader(peer PeerInfo, sessionID, mode, direction string, chunkBytes int, streams []streamStat) string {
	var b strings.Builder
	b.WriteString(rule("Test pornit"))

	client := peer.Label
	if peer.Addr != "" {
		client += " (" + peer.Addr + ")"
	}
	fmt.Fprintf(&b, "   Client:    %s\n", client)
	fmt.Fprintf(&b, "   Sesiune:   %s\n", sessionID)
	fmt.Fprintf(&b, "   Mod:       %s · %s · %s · cadre de %s\n",
		modeLabel(mode), directionLabel(direction), streamCount(len(streams)), FormatBytes(int64(chunkBytes)))

	if len(streams) == 0 {
		b.WriteString("   Streamuri: (niciunul)\n")
		return b.String()
	}

	b.WriteString("   Streamuri:\n")
	for _, s := range sortedByIndex(streams) {
		fmt.Fprintf(&b, "     #%d  %s → %s\n", s.Index, s.From, s.To)
	}
	return b.String()
}

// formatStreamTable renders the per-stream breakdown printed at the end of a
// phase. showStalls is false for upload, where the server is reading and has no
// write to be blocked on.
func formatStreamTable(title string, rows []streamStat, showStalls bool) string {
	var b strings.Builder
	b.WriteString(rule(title))

	if len(rows) == 0 {
		b.WriteString("   (niciun stream)\n")
		return b.String()
	}

	var total, frames int64
	var stalls int
	var maxElapsed int64
	for _, r := range rows {
		total += r.Bytes
		frames += r.Frames
		stalls += r.Stalls
		if r.ElapsedMs > maxElapsed {
			maxElapsed = r.ElapsedMs
		}
	}

	header := fmt.Sprintf("     %-4s %-12s %-10s %-9s %6s", "#", "Octeți", "Cadre", "Mbps", "Cotă")
	if showStalls {
		header += "   Blocaje"
	}
	b.WriteString(header + "\n")

	for _, r := range sortedByIndex(rows) {
		share := 0.0
		if total > 0 {
			share = float64(r.Bytes) / float64(total) * 100
		}
		fmt.Fprintf(&b, "     %-4d %-12s %-10s %-9s %5.1f%%",
			r.Index, FormatBytes(r.Bytes), formatThousands(r.Frames), trimUnit(FormatMbps(r.Mbps())), share)
		if showStalls {
			fmt.Fprintf(&b, "   %7d", r.Stalls)
		}
		b.WriteString("\n")
	}

	agg := Mbps(total, time.Duration(maxElapsed)*time.Millisecond)
	fmt.Fprintf(&b, "     %-4s %-12s %-10s %-9s %6s",
		"tot", FormatBytes(total), formatThousands(frames), trimUnit(FormatMbps(agg)), "")
	if showStalls {
		fmt.Fprintf(&b, "   %7d", stalls)
	}
	b.WriteString("\n")

	// An even split is the healthy case; call out the outlier when it is not.
	if len(rows) > 1 && total > 0 {
		if note := balanceNote(rows, total); note != "" {
			b.WriteString("   " + note + "\n")
		}
	}
	return b.String()
}

// balanceNote flags a lopsided split. Perfectly even is 100/n percent each; a
// stream far off that is worth a word, because it usually means a wedged
// connection or a congested queue rather than a slow network.
func balanceNote(rows []streamStat, total int64) string {
	n := float64(len(rows))
	even := 100 / n
	worstLow, worstHigh := 100.0, 0.0
	var lowIdx, highIdx int
	for _, r := range rows {
		share := float64(r.Bytes) / float64(total) * 100
		if share < worstLow {
			worstLow, lowIdx = share, r.Index
		}
		if share > worstHigh {
			worstHigh, highIdx = share, r.Index
		}
	}
	// Half the even share, or double it, is well outside normal TCP variation.
	if worstLow < even/2 {
		return fmt.Sprintf("stream #%d a luat doar %.1f%% din trafic (repartiție uniformă ar fi %.1f%%)",
			lowIdx, worstLow, even)
	}
	if worstHigh > even*2 {
		return fmt.Sprintf("stream #%d a luat %.1f%% din trafic (repartiție uniformă ar fi %.1f%%)",
			highIdx, worstHigh, even)
	}
	return ""
}

func sortedByIndex(rows []streamStat) []streamStat {
	out := append([]streamStat(nil), rows...)
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// rule draws a titled separator with a clock, so consecutive runs stay readable
// in a scroll-back buffer. These blocks go to stdout like the startup banner
// rather than through log, which would prefix only the first line and leave the
// rest hanging.
func rule(title string) string {
	line := "\n── " + time.Now().Format("15:04:05") + " " + title + " "
	if pad := 60 - len([]rune(line)); pad > 0 {
		line += strings.Repeat("─", pad)
	}
	return line + "\n"
}

func modeLabel(mode string) string {
	if mode == ModeManual {
		return "manual, până la Stop"
	}
	return "automat, 10 s pe direcție"
}

func directionLabel(dir string) string {
	switch dir {
	case DirectionDownload:
		return "doar download"
	case DirectionUpload:
		return "doar upload"
	}
	return "download și upload"
}

func streamCount(n int) string {
	if n == 1 {
		return "1 stream"
	}
	return fmt.Sprintf("%d streamuri", n)
}

// formatThousands groups digits so a six figure frame count stays readable.
func formatThousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ' ')
		}
		out = append(out, c)
	}
	return string(out)
}

// trimUnit drops the " Mbps" suffix for table cells that already have a column
// header saying so.
func trimUnit(s string) string {
	return strings.TrimSuffix(s, " Mbps")
}
