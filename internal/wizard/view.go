package wizard

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// ANSI-only palette - matches the colors used in internal/tui.
const (
	ansiRed         = "1"
	ansiGreen       = "2"
	ansiYellow      = "3"
	ansiBlue        = "4"
	ansiCyan        = "6"
	ansiBrightBlack = "8"
)

func init() {
	lipgloss.SetColorProfile(termenv.ANSI)
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	maxRepoPreview = 6
)

// View renders the wizard.
func (m Model) View() string {
	width := m.width
	if width < 60 {
		width = 60
	}

	var content strings.Builder
	content.WriteString(m.renderModeStep())
	content.WriteString("\n")
	content.WriteString(dimStyle().Render("│"))
	content.WriteString("\n")
	content.WriteString(m.renderRepoStep())

	box := renderBox("Setup", strings.TrimRight(content.String(), "\n"), width)

	var out strings.Builder
	out.WriteString(setTerminalTitle(m.terminalTitle()))
	out.WriteString(box)
	out.WriteString("\n")

	if bar := m.renderActionBar(); bar != "" {
		out.WriteString("\n")
		out.WriteString(bar)
		out.WriteString("\n")
	}

	out.WriteString("\n")
	out.WriteString(m.renderFooter())
	out.WriteString("\n")

	if m.fetchErr != nil && m.screen == screenBulkError {
		out.WriteString("\n")
		out.WriteString(redStyle().Render("error: " + m.fetchErr.Error()))
		out.WriteString("\n")
	}

	return out.String()
}

func (m Model) renderModeStep() string {
	icon, style := m.modeStepIconStyle()
	header := style.Render(icon) + " " + boldStyle().Render("Mode")

	if m.screen == screenMode {
		var lines []string
		lines = append(lines, header)
		for i, opt := range modeOptions {
			marker := "  "
			label := opt.label()
			line := fmt.Sprintf("    %d. %s", i+1, label)
			if i == m.selectedIdx {
				line = boldStyle().Render(fmt.Sprintf("  ▸ %d. %s", i+1, label))
				_ = marker
			}
			lines = append(lines, line)
		}
		return strings.Join(lines, "\n")
	}

	return header + "  " + dimStyle().Render(m.mode.label())
}

func (m Model) renderRepoStep() string {
	icon, style := m.repoStepIconStyle()
	header := style.Render(icon) + " " + boldStyle().Render("Repos")

	switch m.screen {
	case screenMode:
		return header

	case screenFetching:
		return header + "  " + dimStyle().Render("asking github for your repos…")

	case screenBulkConfirm:
		return header + "  " + dimStyle().Render(bulkSummary(m.fetched))

	case screenBulkEmpty:
		return header + "  " + yellowStyle().Render("no repos found for that filter")

	case screenBulkError:
		return header + "  " + redStyle().Render(truncate(m.fetchErr.Error(), 60))

	case screenDetected:
		return header + "  " + dimStyle().Render("detected: ") + m.cfg.DetectedRepo

	case screenManual:
		return header + "  " + m.input.View()

	case screenDone:
		switch m.mode {
		case ModeOneAtATime:
			if len(m.repos) == 1 {
				return header + "  " + dimStyle().Render(m.repos[0])
			}
		default:
			return header + "  " + dimStyle().Render(fmt.Sprintf("%d added", len(m.repos)))
		}
	}
	return header
}

func bulkSummary(repos []string) string {
	if len(repos) == 0 {
		return "no repos"
	}
	preview := repos
	suffix := ""
	if len(repos) > maxRepoPreview {
		preview = repos[:maxRepoPreview]
		suffix = fmt.Sprintf(", and %d more", len(repos)-maxRepoPreview)
	}
	noun := "repos"
	if len(repos) == 1 {
		noun = "repo"
	}
	return fmt.Sprintf("%d %s: %s%s", len(repos), noun, strings.Join(preview, ", "), suffix)
}

func (m Model) renderActionBar() string {
	switch m.screen {
	case screenMode:
		return " " + boldStyle().Render("1/2/3/4") + " select  " + boldStyle().Render("↑↓") + " move  " + boldStyle().Render("⏎") + " confirm"
	case screenBulkConfirm:
		return " " + boldStyle().Render("y") + " add all  " + boldStyle().Render("n") + " back"
	case screenBulkEmpty:
		return " " + boldStyle().Render("b") + " back  " + boldStyle().Render("r") + " retry"
	case screenBulkError:
		return " " + boldStyle().Render("r") + " retry  " + boldStyle().Render("b") + " back"
	case screenDetected:
		return " " + boldStyle().Render("y") + " add this  " + boldStyle().Render("m") + " enter manually"
	case screenManual:
		return " " + boldStyle().Render("⏎") + " submit  " + boldStyle().Render("esc") + " back"
	}
	return ""
}

func (m Model) renderFooter() string {
	return " " + boldStyle().Render("q") + " quit"
}

func (m Model) terminalTitle() string {
	switch {
	case m.success:
		return "✓ ezoss init"
	case m.aborted:
		return "✗ ezoss init"
	}
	return "○ ezoss init"
}

func setTerminalTitle(title string) string {
	return "\x1b]2;" + title + "\x07"
}

func (m Model) modeStepIconStyle() (string, lipgloss.Style) {
	if m.mode != ModeNone {
		return "✓", greenStyle()
	}
	return "⏸", yellowStyle()
}

func (m Model) repoStepIconStyle() (string, lipgloss.Style) {
	switch m.screen {
	case screenMode:
		return "○", dimStyle()
	case screenFetching:
		if len(spinnerFrames) == 0 {
			return "◉", blueStyle()
		}
		return spinnerFrames[m.spinnerFrame%len(spinnerFrames)], blueStyle()
	case screenBulkConfirm, screenDetected, screenManual:
		return "⏸", yellowStyle()
	case screenBulkEmpty:
		return "–", yellowStyle()
	case screenBulkError:
		return "✗", redStyle()
	case screenDone:
		return "✓", greenStyle()
	}
	return "○", dimStyle()
}

func dimStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(ansiBrightBlack))
}
func greenStyle() lipgloss.Style  { return lipgloss.NewStyle().Foreground(lipgloss.Color(ansiGreen)) }
func redStyle() lipgloss.Style    { return lipgloss.NewStyle().Foreground(lipgloss.Color(ansiRed)) }
func yellowStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(ansiYellow)) }
func blueStyle() lipgloss.Style   { return lipgloss.NewStyle().Foreground(lipgloss.Color(ansiBlue)) }
func cyanStyle() lipgloss.Style   { return lipgloss.NewStyle().Foreground(lipgloss.Color(ansiCyan)) }
func boldStyle() lipgloss.Style   { return lipgloss.NewStyle().Bold(true) }

// renderBox draws a rounded-border box with a cyan bold title embedded in
// the top border. Mirrors internal/tui/box.go for visual consistency.
func renderBox(title, content string, width int) string {
	if width < 6 {
		width = 6
	}
	titleStyled := cyanStyle().Bold(true).Render(title)
	borderColor := dimStyle()

	titleWidth := lipgloss.Width(titleStyled)
	fillWidth := width - 5 - titleWidth
	if fillWidth < 1 {
		fillWidth = 1
	}
	topBorder := borderColor.Render("╭─ ") + titleStyled + " " + borderColor.Render(strings.Repeat("─", fillWidth)+"╮")

	contentWidth := width - 4
	if contentWidth < 1 {
		contentWidth = 1
	}

	var lines []string
	for _, cl := range strings.Split(content, "\n") {
		visWidth := lipgloss.Width(cl)
		pad := contentWidth - visWidth
		if pad < 0 {
			pad = 0
		}
		line := borderColor.Render("│") + " " + cl + strings.Repeat(" ", pad) + " " + borderColor.Render("│")
		lines = append(lines, line)
	}

	fill := width - 2
	if fill < 1 {
		fill = 1
	}
	bottom := borderColor.Render("╰" + strings.Repeat("─", fill) + "╯")

	return topBorder + "\n" + strings.Join(lines, "\n") + "\n" + bottom
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 1 {
		return ""
	}
	return s[:n-1] + "…"
}
