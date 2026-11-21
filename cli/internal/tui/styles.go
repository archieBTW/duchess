package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	colorBg       = lipgloss.Color("#0B0F17")
	colorSurface  = lipgloss.Color("#121926")
	colorElevated = lipgloss.Color("#1A2334")
	colorBorder   = lipgloss.Color("#253248")
	colorCyan     = lipgloss.Color("#38BDF8")
	colorEmerald  = lipgloss.Color("#10B981")
	colorAmber    = lipgloss.Color("#F59E0B")
	colorRose     = lipgloss.Color("#F43F5E")
	colorViolet   = lipgloss.Color("#A78BFA")
	colorText     = lipgloss.Color("#F1F5F9")
	colorSubtext  = lipgloss.Color("#94A3B8")
	colorMuted    = lipgloss.Color("#64748B")

	styleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorCyan)

	styleSubtitle = lipgloss.NewStyle().
			Foreground(colorSubtext)

	styleMuted = lipgloss.NewStyle().
			Foreground(colorMuted)

	styleAccent = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorViolet)

	styleBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	styleActiveBox = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorCyan).
			Padding(0, 1)

	styleModal = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(colorCyan).
			Background(colorSurface).
			Padding(1, 2)

	styleTabActive = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorBg).
			Background(colorCyan).
			Padding(0, 2)

	styleTabInactive = lipgloss.NewStyle().
				Foreground(colorSubtext).
				Background(colorElevated).
				Padding(0, 2)

	styleSubTabActive = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorCyan).
				Underline(true).
				Padding(0, 1)

	styleSubTabInactive = lipgloss.NewStyle().
				Foreground(colorMuted).
				Padding(0, 1)

	styleSelectedRow = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorText).
				Background(colorElevated)

	styleFooter = lipgloss.NewStyle().
			Foreground(colorSubtext).
			Background(colorSurface).
			Padding(0, 1)
)

func badge(label string, fg lipgloss.Color) string {
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(fg).
		Render("[" + label + "]")
}

func metricColor(pct float64) lipgloss.Color {
	switch {
	case pct >= 85:
		return colorRose
	case pct >= 65:
		return colorAmber
	default:
		return colorEmerald
	}
}

// asciiGauge renders a crisp ASCII/block meter like `[██████░░░░] 61.4%`.
func asciiGauge(pct float64, width int) string {
	if width < 6 {
		width = 6
	}
	clamped := pct
	if clamped < 0 {
		clamped = 0
	}
	if clamped > 100 {
		clamped = 100
	}
	filled := int(clamped / 100.0 * float64(width))
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	col := metricColor(clamped)
	coloredBar := lipgloss.NewStyle().Foreground(col).Render(bar)
	return fmt.Sprintf("[%s] %5.1f%%", coloredBar, clamped)
}

// asciiSparkline renders recent percentage samples as a compact sparkline.
func asciiSparkline(samples []float64, maxLen int) string {
	if len(samples) == 0 {
		return styleMuted.Render(strings.Repeat("·", minInt(maxLen, 10)))
	}
	blocks := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	start := 0
	if len(samples) > maxLen {
		start = len(samples) - maxLen
	}
	var b strings.Builder
	for _, s := range samples[start:] {
		v := s
		if v < 0 {
			v = 0
		}
		if v > 100 {
			v = 100
		}
		idx := int(v / 100.0 * float64(len(blocks)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		b.WriteRune(blocks[idx])
	}
	last := samples[len(samples)-1]
	return lipgloss.NewStyle().Foreground(metricColor(last)).Render(b.String())
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

// clampLine ensures a possibly ANSI-styled string never exceeds maxW display cells
// and never contains embedded newlines, preventing unexpected line wrapping in tables.
func clampLine(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	s = strings.ReplaceAll(s, "\r", "")
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	if ansi.StringWidth(s) <= maxW {
		return s
	}
	return ansi.Truncate(s, maxW, "…")
}

// padCell truncates and right-pads plain text to exact width before styling.
func padCell(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", " ")
	s = truncate(s, w)
	diff := w - ansi.StringWidth(s)
	if diff > 0 {
		return s + strings.Repeat(" ", diff)
	}
	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
