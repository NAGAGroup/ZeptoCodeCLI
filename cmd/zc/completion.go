// As-you-type completion dropdown for "/" commands and "@" paths, rendered
// between the input and the statusline. Tab or → accepts, ↑/↓ navigate.
package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type completionState struct {
	visible bool
	items   []completionItem
	sel     int
	// what accepting replaces: the input prefix before the fragment and the
	// suffix after it (completion only edits the fragment between them)
	prefix string
	suffix string
}

type completionItem struct {
	insert string // text that replaces the fragment
	label  string
	desc   string
}

// fuzzyScore: subsequence match, lower is better; -1 = no match.
func fuzzyScore(needle, hay string) int {
	needle, hay = strings.ToLower(needle), strings.ToLower(hay)
	if needle == "" {
		return 0
	}
	score, j := 0, 0
	for i := 0; i < len(hay) && j < len(needle); i++ {
		if hay[i] == needle[j] {
			j++
		} else {
			score++
		}
	}
	if j < len(needle) {
		return -1
	}
	return score
}

// refreshCompletions recomputes the dropdown from the current input value.
func (m *model) refreshCompletions() {
	m.completion = completionState{}
	val := m.input.Value()
	if val == "" || strings.Contains(val, "\n") {
		return
	}

	// Slash commands: only while typing the command word itself.
	if strings.HasPrefix(val, "/") && !strings.Contains(val, " ") {
		frag := strings.TrimPrefix(val, "/")
		type scored struct {
			it    completionItem
			score int
		}
		var out []scored
		for _, c := range m.knownCommands() {
			if s := fuzzyScore(frag, c.id); s >= 0 {
				out = append(out, scored{completionItem{
					insert: "/" + c.id + " ",
					label:  "/" + c.id,
					desc:   c.desc,
				}, s})
			}
		}
		sort.SliceStable(out, func(a, b int) bool { return out[a].score < out[b].score })
		for i, s := range out {
			if i >= 8 {
				break
			}
			m.completion.items = append(m.completion.items, s.it)
		}
		m.completion.visible = len(m.completion.items) > 0
		m.completion.prefix, m.completion.suffix = "", ""
		return
	}

	// @-paths: last whitespace-separated token.
	idx := strings.LastIndexAny(val, " \t")
	last := val[idx+1:]
	if !strings.HasPrefix(last, "@") {
		return
	}
	frag := strings.TrimPrefix(last, "@")
	base := m.serverCWD
	if base == "" {
		base, _ = os.Getwd()
	}
	dir, partial := filepath.Split(frag)
	list, err := os.ReadDir(filepath.Join(base, dir))
	if err != nil {
		return
	}
	for _, de := range list {
		name := de.Name()
		if partial != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(partial)) {
			continue
		}
		if partial == "" && strings.HasPrefix(name, ".") {
			continue
		}
		if de.IsDir() {
			name += "/"
		}
		m.completion.items = append(m.completion.items, completionItem{
			insert: "@" + dir + name,
			label:  name,
		})
		if len(m.completion.items) >= 8 {
			break
		}
	}
	m.completion.visible = len(m.completion.items) > 0
	m.completion.prefix = val[:idx+1]
	m.completion.suffix = ""
}

// acceptCompletion applies the selected item to the input.
func (m *model) acceptCompletion() {
	c := &m.completion
	if !c.visible || c.sel >= len(c.items) {
		return
	}
	m.input.SetValue(c.prefix + c.items[c.sel].insert + c.suffix)
	m.input.CursorEnd()
	m.refreshCompletions()
}

func (m *model) renderCompletions() string {
	c := &m.completion
	if !c.visible {
		return ""
	}
	var b strings.Builder
	for i, it := range c.items {
		line := it.label
		if it.desc != "" {
			line += "  " + compactOneLine(it.desc, m.width-len(it.label)-10)
		}
		if i == c.sel {
			b.WriteString(styleCompletionSel.Render("▸ " + line))
		} else {
			b.WriteString(styleCompletion.Render("  " + line))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
