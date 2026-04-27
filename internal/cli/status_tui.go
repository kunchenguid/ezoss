package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	statusTUIRefreshInterval = 100 * time.Millisecond
	statusTUICollectInterval = 2 * time.Second
)

type statusTUIOptions struct {
	RefreshInterval time.Duration
	CollectInterval time.Duration
	Collect         func() (statusData, error)
	Now             func() time.Time
}

var runStatusTUI = func(ctx context.Context, out io.Writer, errOut io.Writer, opts statusTUIOptions) error {
	return runStatusTUIProgram(ctx, out, errOut, opts)
}

type statusTUIModel struct {
	data            statusData
	err             error
	hasData         bool
	collect         func() (statusData, error)
	now             func() time.Time
	refreshInterval time.Duration
	collectInterval time.Duration
	width           int
	height          int
	showHelp        bool
}

type statusTUITickMsg struct{}

type statusTUICollectMsg struct{}

type statusTUIDataMsg struct {
	data statusData
	err  error
}

func runStatusTUIProgram(ctx context.Context, out io.Writer, errOut io.Writer, opts statusTUIOptions) error {
	_ = errOut
	p := tea.NewProgram(newStatusTUIModel(opts), tea.WithContext(ctx), tea.WithAltScreen(), tea.WithOutput(out))
	_, err := p.Run()
	return err
}

func newStatusTUIModel(opts statusTUIOptions) statusTUIModel {
	if opts.RefreshInterval <= 0 {
		opts.RefreshInterval = statusTUIRefreshInterval
	}
	if opts.CollectInterval <= 0 {
		opts.CollectInterval = statusTUICollectInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return statusTUIModel{
		collect:         opts.Collect,
		now:             opts.Now,
		refreshInterval: opts.RefreshInterval,
		collectInterval: opts.CollectInterval,
	}
}

func (m statusTUIModel) Init() tea.Cmd {
	return tea.Batch(collectStatusTUICmd(m.collect), scheduleStatusTUITick(m.refreshInterval))
}

func (m statusTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "?":
			m.showHelp = !m.showHelp
			return m, nil
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case statusTUITickMsg:
		return m, scheduleStatusTUITick(m.refreshInterval)
	case statusTUICollectMsg:
		return m, collectStatusTUICmd(m.collect)
	case statusTUIDataMsg:
		m.err = msg.err
		if msg.err == nil {
			m.data = msg.data
			m.hasData = true
		}
		return m, scheduleStatusTUICollect(m.collectInterval)
	}
	return m, nil
}

func (m statusTUIModel) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	if m.height > 0 && m.height < 3 {
		return statusClipLine("Status", width)
	}
	help := ""
	if m.showHelp {
		help = statusRenderBox(width, "Keyboard shortcuts", strings.Join([]string{
			"?                  toggle this help",
			"q                  quit",
		}, "\n"))
		if m.height > 0 && statusRenderedLineCount(help)+4 > m.height {
			help = ""
		}
	}
	body := m.renderBody()
	if m.height > 0 {
		maxLines := m.height - 2
		if help != "" {
			maxLines -= statusRenderedLineCount(help) + 1
		}
		body = statusTruncateBody(body, maxLines)
	}
	footer := statusMetaStyle().Render("q quit  ? help")
	sections := []string{statusRenderBoxWithFooter(width, "Status", body, footer)}
	if help != "" {
		sections = append(sections, help)
	}
	return strings.Join(sections, "\n\n")
}

func (m statusTUIModel) renderBody() string {
	var b strings.Builder
	if m.err != nil {
		fmt.Fprintf(&b, "%s\n", statusErrorStyle().Render("error: "+m.err.Error()))
		if m.hasData {
			fmt.Fprintln(&b)
		}
	}
	if !m.hasData {
		if m.err == nil {
			fmt.Fprintln(&b, "loading...")
		}
		return b.String()
	}

	fmt.Fprint(&b, renderRichStatus(m.data, m.now()))
	return b.String()
}

func statusRenderBox(width int, title string, body string) string {
	return statusRenderBoxWithFooter(width, title, body, "")
}

func statusTruncateBody(body string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(body, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= maxLines {
		return body
	}
	if maxLines == 1 {
		return fmt.Sprintf("... %d more lines\n", len(lines))
	}
	omitted := len(lines) - maxLines + 1
	visible := append([]string(nil), lines[:maxLines-1]...)
	visible = append(visible, fmt.Sprintf("... %d more lines", omitted))
	return strings.Join(visible, "\n") + "\n"
}

func statusRenderBoxWithFooter(width int, title string, body string, footer string) string {
	if width < 6 {
		width = 6
	}
	border := statusBorderStyle()
	title = statusClipLine(title, width-6)
	styledTitle := statusTitleStyle().Render(title)

	titleW := lipgloss.Width(styledTitle)
	fillW := width - 5 - titleW
	if fillW < 1 {
		fillW = 1
	}
	top := border.Render("╭─ ") + styledTitle + " " + border.Render(strings.Repeat("─", fillW)+"╮")

	contentWidth := width - 4
	if contentWidth < 1 {
		contentWidth = 1
	}

	rawLines := strings.Split(body, "\n")
	if len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" {
		rawLines = rawLines[:len(rawLines)-1]
	}
	lines := make([]string, 0, len(rawLines))
	for _, cl := range rawLines {
		cl = statusClipLine(cl, contentWidth)
		visW := lipgloss.Width(cl)
		pad := contentWidth - visW
		if pad < 0 {
			pad = 0
		}
		lines = append(lines, border.Render("│")+" "+cl+strings.Repeat(" ", pad)+" "+border.Render("│"))
	}
	return top + "\n" + strings.Join(lines, "\n") + "\n" + statusRenderBottomBorder(width, footer)
}

func statusRenderedLineCount(s string) int {
	if s == "" {
		return 0
	}
	return len(strings.Split(strings.TrimRight(s, "\n"), "\n"))
}

func statusClipLine(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	if width <= 3 {
		return ansi.Truncate(s, width, "")
	}
	return ansi.Truncate(s, width, "...")
}

func statusRenderBottomBorder(width int, footer string) string {
	border := statusBorderStyle()
	if footer == "" {
		fill := width - 2
		if fill < 1 {
			fill = 1
		}
		return border.Render("╰" + strings.Repeat("─", fill) + "╯")
	}
	footer = statusClipLine(footer, width-7)
	rw := lipgloss.Width(footer)
	trail := width - rw - 6
	if trail < 1 {
		trail = 1
	}
	return border.Render("╰── ") + footer + " " + border.Render(strings.Repeat("─", trail)+"╯")
}

func statusBorderStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
}

func statusTitleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
}

func statusMetaStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
}

func statusErrorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
}

func collectStatusTUICmd(collect func() (statusData, error)) tea.Cmd {
	return func() tea.Msg {
		if collect == nil {
			return statusTUIDataMsg{err: fmt.Errorf("status collector is not configured")}
		}
		data, err := collect()
		return statusTUIDataMsg{data: data, err: err}
	}
}

func scheduleStatusTUITick(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return statusTUITickMsg{}
	})
}

func scheduleStatusTUICollect(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(time.Time) tea.Msg {
		return statusTUICollectMsg{}
	})
}
