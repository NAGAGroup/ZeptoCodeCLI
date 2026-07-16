// Pager: a modal scrollable viewer for interactive info — memory files,
// commit diffs — instead of dumping long content into the transcript.
// Scroll with ↑/↓/pgup/pgdn/g/G, dismiss with esc/q/enter.
package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type pager struct {
	title  string
	lines  []string
	offset int
}

func newPager(title, content string) *pager {
	return &pager{title: title, lines: strings.Split(strings.TrimRight(content, "\n"), "\n")}
}

// window is the number of content rows the pager shows.
func (p *pager) window(termH int) int {
	h := termH - 10
	if h < 5 {
		h = 5
	}
	return h
}

func (p *pager) clamp(termH int) {
	maxOff := len(p.lines) - p.window(termH)
	if maxOff < 0 {
		maxOff = 0
	}
	if p.offset > maxOff {
		p.offset = maxOff
	}
	if p.offset < 0 {
		p.offset = 0
	}
}

// handleKey scrolls or closes. Returns false when the pager should close.
func (p *pager) handleKey(msg tea.KeyPressMsg, termH int) bool {
	switch msg.String() {
	case "esc", "q", "enter":
		return false
	case "up", "k":
		p.offset--
	case "down", "j":
		p.offset++
	case "pgup", "ctrl+u":
		p.offset -= p.window(termH)
	case "pgdown", "ctrl+d", "space":
		p.offset += p.window(termH)
	case "g", "home":
		p.offset = 0
	case "G", "end":
		p.offset = len(p.lines)
	}
	p.clamp(termH)
	return true
}

// render draws the pager dialog body.
func (p *pager) render(termW, termH int) string {
	w := dialogWidth(termW)
	win := p.window(termH)
	p.clamp(termH)
	end := p.offset + win
	if end > len(p.lines) {
		end = len(p.lines)
	}
	body := strings.Join(p.lines[p.offset:end], "\n")
	footer := fmt.Sprintf("%d–%d of %d · ↑/↓ pgup/pgdn scroll · esc closes", p.offset+1, end, len(p.lines))
	return dialogBox(p.title, "", body+"\n\n"+styleOverlayDim.Render(footer), w)
}

// diffColorize applies add/remove tints to unified diff text.
func diffColorize(diff string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			b.WriteString(styleDiffAdd.Render(line) + "\n")
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			b.WriteString(styleDiffRemove.Render(line) + "\n")
		default:
			b.WriteString(styleDiffCtx.Render(line) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
