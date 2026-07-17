// Tool cards: per-tool-type transcript rendering after crush's chat tool
// renderers — a status-icon header with humanized name and the tool's most
// telling parameters, plus a truncated, dim result body that ctrl+o
// expands. One renderer, param extraction per tool family.
package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
)

const (
	toolBodyCollapsedLines = 3
	toolBodyExpandedLines  = 30
	// diffContextLines caps the dimmed shared lines shown around a change.
	diffContextLines = 3
)

// humanizeToolName turns snake/camel tool ids into display names.
func humanizeToolName(name string) string {
	switch name {
	case "Bash", "bash":
		return "Bash"
	case "str_replace_editor", "str_replace_based_edit_tool":
		return "Edit"
	case "web_search", "WebSearch":
		return "Web Search"
	case "AskUserQuestion":
		return "Question"
	}
	// snake_case → Title Case; leave CamelCase as-is.
	if strings.Contains(name, "_") {
		words := strings.Split(name, "_")
		for i, w := range words {
			if w != "" {
				words[i] = strings.ToUpper(w[:1]) + w[1:]
			}
		}
		return strings.Join(words, " ")
	}
	return name
}

// toolParams extracts the most telling parameter string for the header.
func toolParams(name string, rawArgs string, serverCWD string) string {
	var args map[string]any
	if json.Unmarshal([]byte(rawArgs), &args) != nil || len(args) == 0 {
		return compactOneLine(rawArgs, 64)
	}
	str := func(key string) string {
		v, _ := args[key].(string)
		return v
	}
	relPath := func(p string) string {
		if p == "" {
			return ""
		}
		if serverCWD != "" {
			if rel, err := filepath.Rel(serverCWD, p); err == nil && !strings.HasPrefix(rel, "..") {
				return rel
			}
		}
		return p
	}
	switch name {
	case "Bash", "bash":
		if d := str("description"); d != "" {
			return d
		}
		return str("command")
	case "Read", "Write", "Edit", "read_file", "write_file", "edit_file":
		p := relPath(str("file_path"))
		if p == "" {
			p = relPath(str("path"))
		}
		return p
	case "Grep", "grep":
		out := str("pattern")
		if p := relPath(str("path")); p != "" {
			out += "  " + p
		}
		return out
	case "Glob", "glob":
		return str("pattern")
	case "Agent", "Task", "agent":
		if d := str("description"); d != "" {
			return d
		}
		return str("subagent_type")
	case "web_search", "WebSearch":
		return str("query")
	case "TaskCreate", "TaskUpdate":
		if s := str("subject"); s != "" {
			return s
		}
	case "memory":
		out := str("command")
		if p := str("file_path"); p != "" {
			out += " " + p
		}
		return out
	}
	// Generic: key=value pairs of short string params.
	var parts []string
	for k, v := range args {
		if s, ok := v.(string); ok && len(s) <= 40 && len(parts) < 3 {
			parts = append(parts, k+"="+s)
		}
	}
	if len(parts) == 0 {
		b, _ := json.Marshal(args)
		return compactOneLine(string(b), 64)
	}
	return strings.Join(parts, " ")
}

// toolStatus classifies a tool_call Line's phase/result into a display state.
func toolStatus(l *protocol.TranscriptLine) string {
	switch l.Phase {
	case "finished":
		if l.ResultOk != nil && !*l.ResultOk {
			return "error"
		}
		return "success"
	case "running":
		return "running"
	default: // streaming | ready
		return ""
	}
}

// phaseDot returns the appropriate phase indicator dot/string for a tool.
func (m *model) phaseDot(l *protocol.TranscriptLine) string {
	switch l.Phase {
	case "streaming":
		// Blink: hide the dot on the "off" half of the tick for a living feel.
		if !m.blinkOn {
			return " "
		}
		return styleToolPhaseStreaming.Render("●")
	case "ready", "running":
		// Blink: hide the dot on the "off" half of the tick for a living feel.
		if !m.blinkOn {
			return " "
		}
		return styleToolPhaseRunning.Render("●")
	case "finished":
		if l.ResultOk != nil && !*l.ResultOk {
			return styleToolPhaseError.Render("●")
		}
		return styleToolPhaseSuccess.Render("●")
	default:
		return styleTool.Render(iconToolPending)
	}
}

// renderToolCard renders a tool_call transcript Line: header + optional body.
func (m *model) renderToolCard(l *protocol.TranscriptLine, w int) string {
	name := humanizeToolName(l.Name)
	params := compactOneLine(toolParams(l.Name, l.ArgsText, m.serverCWD), max(20, w-len(name)-8))

	var suffix string
	nameStyle := styleToolName
	dot := m.phaseDot(l)

	switch toolStatus(l) {
	case "success":
		// dot already green
	case "error":
		suffix = "  " + styleToolErr.Render("error")
	default: // running / streaming args
		if m.isExecuting(l) {
			suffix = "  " + styleAccent.Render("running")
		}
	}
	header := fmt.Sprintf("%s %s %s%s", dot, nameStyle.Render(name), styleToolParam.Render(params), suffix)

	// Per-tool-type specialized rendering.
	body := m.renderToolBodyByType(l, w)
	if body != "" {
		return header + "\n" + body
	}
	return header
}

// renderToolBodyByType dispatches to specialized renderers by tool name.
func (m *model) renderToolBodyByType(l *protocol.TranscriptLine, w int) string {
	if l.ArgsText == "" && l.ResultText == "" {
		return ""
	}

	switch l.Name {
	case "Bash", "bash":
		return m.renderBashTool(l, w)
	case "Edit", "str_replace_editor", "str_replace_based_edit_tool":
		return m.renderEditDiff(l, w)
	case "Write", "write_file":
		return m.renderWriteTool(l, w)
	case "Read", "read_file":
		return m.renderReadTool(l, w)
	case "Grep", "grep":
		return m.renderSearchTool(l, w, "lines")
	case "Glob", "glob":
		return m.renderSearchTool(l, w, "files")
	default:
		return m.renderGenericToolBody(l, w)
	}
}

// renderBashTool renders a bash/shell tool with $ prefix and └ result prefix.
func (m *model) renderBashTool(l *protocol.TranscriptLine, w int) string {
	var args map[string]any
	_ = json.Unmarshal([]byte(l.ArgsText), &args)
	cmd := ""
	if c, ok := args["command"].(string); ok {
		cmd = c
	}

	bodyW := max(10, w-4)
	var out []string

	// Command preview with $ prefix.
	if cmd != "" {
		for _, ln := range strings.Split(cmd, "\n") {
			out = append(out, styleAccent.Render("$ "+compactOneLine(ln, bodyW-2)))
		}
	}

	// Result with └ prefix.
	ret := strings.TrimRight(l.ResultText, "\n")
	if ret != "" {
		lines := strings.Split(ret, "\n")
		maxLines := toolBodyCollapsedLines
		if m.showToolOutput {
			maxLines = toolBodyExpandedLines
		}
		shown := lines
		if len(lines) > maxLines {
			shown = lines[:maxLines]
		}
		for _, ln := range shown {
			if lipgloss.Width(ln) > bodyW {
				ln = compactOneLine(ln, bodyW)
			}
			out = append(out, styleToolBody.Render("└ "+ln))
		}
		if n := len(lines) - len(shown); n > 0 {
			hint := "ctrl+o expands"
			if m.showToolOutput {
				hint = "output capped"
			}
			out = append(out, styleToolBody.Render(fmt.Sprintf("└ … +%d lines (%s)", n, hint)))
		}
	}

	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n")
}

// renderWriteTool renders a file write tool with a "＋ Wrote N lines" summary
// and an all-added content preview whose green background spans the card.
func (m *model) renderWriteTool(l *protocol.TranscriptLine, w int) string {
	var args map[string]any
	_ = json.Unmarshal([]byte(l.ArgsText), &args)
	path := str(args["file_path"])
	if path == "" {
		path = str(args["path"])
	}
	content := str(args["content"])

	bodyW := max(10, w-4)
	var out []string

	// Summary line: "＋ Wrote N lines to <path>" (trailing newline not counted).
	lines := strings.Split(content, "\n")
	lineCount := len(lines)
	if lineCount > 0 && lines[len(lines)-1] == "" {
		lineCount--
	}
	if path != "" {
		plus := lipgloss.NewStyle().Foreground(theme.OK).Bold(true).Render("＋")
		out = append(out, plus+" "+styleToolName.Render(fmt.Sprintf("Wrote %d lines to %s", lineCount, path)))
	}

	// Content preview (first few lines), dropping a trailing empty line.
	preview := lines
	if len(preview) > 0 && preview[len(preview)-1] == "" {
		preview = preview[:len(preview)-1]
	}
	maxLines := toolBodyCollapsedLines
	if m.showToolOutput {
		maxLines = toolBodyExpandedLines
	}
	shown := preview
	if len(preview) > maxLines {
		shown = preview[:maxLines]
	}
	for _, ln := range shown {
		out = append(out, diffRow(styleDiffAdd, "+ ", ln, bodyW))
	}
	if n := len(preview) - len(shown); n > 0 {
		hint := "ctrl+o expands"
		if m.showToolOutput {
			hint = "output capped"
		}
		out = append(out, styleToolBody.Render(fmt.Sprintf("… +%d lines (%s)", n, hint)))
	}

	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n")
}

// renderReadTool renders a file read tool with line count.
func (m *model) renderReadTool(l *protocol.TranscriptLine, w int) string {
	var args map[string]any
	_ = json.Unmarshal([]byte(l.ArgsText), &args)
	path := ""
	if p, ok := args["file_path"].(string); ok && p != "" {
		path = p
	} else if p, ok := args["path"].(string); ok && p != "" {
		path = p
	}

	ret := strings.TrimRight(l.ResultText, "\n")
	if ret == "" && path == "" {
		return ""
	}

	bodyW := max(10, w-4)
	var out []string

	// Show line count if we have content.
	if ret != "" {
		lines := strings.Split(ret, "\n")
		lineCount := len(lines)
		if lineCount > 0 && lines[len(lines)-1] == "" {
			lineCount--
		}
		if path != "" {
			out = append(out, styleToolParam.Render(fmt.Sprintf("Read %d lines from %s", lineCount, path)))
		}

		maxLines := toolBodyCollapsedLines
		if m.showToolOutput {
			maxLines = toolBodyExpandedLines
		}
		shown := lines
		if len(lines) > maxLines {
			shown = lines[:maxLines]
		}
		for _, ln := range shown {
			if lipgloss.Width(ln) > bodyW {
				ln = compactOneLine(ln, bodyW)
			}
			out = append(out, styleToolBody.Render("└ "+ln))
		}
		if n := len(lines) - len(shown); n > 0 {
			hint := "ctrl+o expands"
			if m.showToolOutput {
				hint = "output capped"
			}
			out = append(out, styleToolBody.Render(fmt.Sprintf("└ … +%d lines (%s)", n, hint)))
		}
	} else if path != "" {
		out = append(out, styleToolParam.Render("Reading "+path))
	}

	return strings.Join(out, "\n")
}

// renderSearchTool renders grep/glob tools with a "Found N" summary.
func (m *model) renderSearchTool(l *protocol.TranscriptLine, w int, unit string) string {
	ret := strings.TrimRight(l.ResultText, "\n")
	if ret == "" {
		return ""
	}

	lines := strings.Split(ret, "\n")
	count := len(lines)
	if count > 0 && lines[len(lines)-1] == "" {
		count--
	}

	bodyW := max(10, w-4)
	var out []string
	out = append(out, styleToolName.Render(fmt.Sprintf("Found %d %s", count, unit)))

	maxLines := toolBodyCollapsedLines
	if m.showToolOutput {
		maxLines = toolBodyExpandedLines
	}
	shown := lines
	if len(lines) > maxLines {
		shown = lines[:maxLines]
	}
	for _, ln := range shown {
		if lipgloss.Width(ln) > bodyW {
			ln = compactOneLine(ln, bodyW)
		}
		out = append(out, styleToolBody.Render("└ "+ln))
	}
	if n := len(lines) - len(shown); n > 0 {
		hint := "ctrl+o expands"
		if m.showToolOutput {
			hint = "output capped"
		}
		out = append(out, styleToolBody.Render(fmt.Sprintf("└ … +%d lines (%s)", n, hint)))
	}

	return strings.Join(out, "\n")
}

// renderGenericToolBody renders the truncated result with └ L-bracket prefix.
func (m *model) renderGenericToolBody(l *protocol.TranscriptLine, w int) string {
	ret := strings.TrimRight(l.ResultText, "\n")
	if ret == "" {
		return ""
	}
	lines := strings.Split(ret, "\n")
	maxLines := toolBodyCollapsedLines
	if m.showToolOutput {
		maxLines = toolBodyExpandedLines
	}
	shown := lines
	if len(lines) > maxLines {
		shown = lines[:maxLines]
	}
	var b strings.Builder
	bodyW := max(10, w-4)
	for _, ln := range shown {
		if lipgloss.Width(ln) > bodyW {
			ln = compactOneLine(ln, bodyW)
		}
		b.WriteString(styleToolBody.Render("└ "+ln) + "\n")
	}
	if n := len(lines) - len(shown); n > 0 {
		hint := "ctrl+o expands"
		if m.showToolOutput {
			hint = "output capped"
		}
		b.WriteString(styleToolBody.Render(fmt.Sprintf("└ … +%d lines (%s)", n, hint)) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderEditDiff builds a unified-style +/- diff for Edit/MultiEdit tool cards
// from the tool arguments (old_string→new_string). Shared leading/trailing
// lines show dimmed as context; only the changed lines wear +/- and a tinted
// full-width background.
func (m *model) renderEditDiff(l *protocol.TranscriptLine, w int) string {
	if l.ArgsText == "" {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(l.ArgsText), &args); err != nil {
		return ""
	}
	type edit struct{ oldS, newS string }
	var edits []edit
	switch l.Name {
	case "Edit", "str_replace_editor", "str_replace_based_edit_tool":
		edits = append(edits, edit{str(args["old_string"]), str(args["new_string"])})
	case "MultiEdit":
		if raw, ok := args["edits"].([]any); ok {
			for _, e := range raw {
				if em, ok := e.(map[string]any); ok {
					edits = append(edits, edit{str(em["old_string"]), str(em["new_string"])})
				}
			}
		}
	default:
		return ""
	}

	bodyW := max(10, w-4)
	var out []string
	for _, e := range edits {
		out = append(out, diffEdit(e.oldS, e.newS, bodyW)...)
	}
	if len(out) == 0 {
		return ""
	}

	maxLines := toolBodyCollapsedLines
	if m.showToolOutput {
		maxLines = toolBodyExpandedLines
	}
	shown := out
	if len(out) > maxLines {
		shown = out[:maxLines]
	}
	res := strings.Join(shown, "\n")
	if n := len(out) - len(shown); n > 0 {
		hint := "ctrl+o expands"
		if m.showToolOutput {
			hint = "diff capped"
		}
		res += "\n" + styleToolBody.Render(fmt.Sprintf("… +%d lines (%s)", n, hint))
	}
	return res
}

// diffEdit turns one old→new change into unified-diff rows: a few dimmed
// context lines around the change, removed lines prefixed "-", added lines
// prefixed "+". A clean single-line change gets word-level emphasis.
func diffEdit(oldS, newS string, bodyW int) []string {
	oldL := splitDiffLines(oldS)
	newL := splitDiffLines(newS)

	// Longest common prefix / suffix at line granularity → context.
	i := 0
	for i < len(oldL) && i < len(newL) && oldL[i] == newL[i] {
		i++
	}
	j := 0
	for j < len(oldL)-i && j < len(newL)-i && oldL[len(oldL)-1-j] == newL[len(newL)-1-j] {
		j++
	}
	lead := oldL[:i]
	del := oldL[i : len(oldL)-j]
	add := newL[i : len(newL)-j]
	var tail []string
	if j > 0 {
		tail = oldL[len(oldL)-j:]
	}

	var out []string
	for _, ln := range lastN(lead, diffContextLines) {
		out = append(out, diffRow(styleDiffCtx, "  ", ln, bodyW))
	}

	// Single-line replacement → word-level emphasis when it fits cleanly.
	if len(del) == 1 && len(add) == 1 {
		if d, a, ok := diffRowsEmph(del[0], add[0], bodyW); ok {
			out = append(out, d, a)
		} else {
			out = append(out, diffRow(styleDiffRemove, "- ", del[0], bodyW))
			out = append(out, diffRow(styleDiffAdd, "+ ", add[0], bodyW))
		}
	} else {
		for _, ln := range del {
			out = append(out, diffRow(styleDiffRemove, "- ", ln, bodyW))
		}
		for _, ln := range add {
			out = append(out, diffRow(styleDiffAdd, "+ ", ln, bodyW))
		}
	}

	for _, ln := range firstN(tail, diffContextLines) {
		out = append(out, diffRow(styleDiffCtx, "  ", ln, bodyW))
	}
	return out
}

// diffRow renders one full-width diff row: prefix + truncated text, padded
// with spaces so the style's background spans the whole card width.
func diffRow(style lipgloss.Style, prefix, text string, bodyW int) string {
	text = truncCell(text, max(1, bodyW-lipgloss.Width(prefix)))
	line := prefix + text
	if pad := bodyW - lipgloss.Width(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	return style.Render(line)
}

// diffRowsEmph renders a single-line change with the differing middle segment
// emphasized (bold+underline). It bails (ok=false) when the lines would need
// truncation or differ entirely, so the caller can fall back to plain rows.
func diffRowsEmph(oldLine, newLine string, bodyW int) (string, string, bool) {
	ol := strings.ReplaceAll(oldLine, "\t", "    ")
	nl := strings.ReplaceAll(newLine, "\t", "    ")
	if lipgloss.Width(ol)+2 > bodyW || lipgloss.Width(nl)+2 > bodyW {
		return "", "", false
	}
	or := []rune(ol)
	nr := []rune(nl)
	p := 0
	for p < len(or) && p < len(nr) && or[p] == nr[p] {
		p++
	}
	s := 0
	for s < len(or)-p && s < len(nr)-p && or[len(or)-1-s] == nr[len(nr)-1-s] {
		s++
	}
	// No shared context → emphasis adds nothing; let the caller do plain rows.
	if p == 0 && s == 0 {
		return "", "", false
	}

	build := func(base lipgloss.Style, prefix, pre, mid, suf string) string {
		content := prefix + pre + mid + suf
		tailPad := ""
		if pad := bodyW - lipgloss.Width(content); pad > 0 {
			tailPad = strings.Repeat(" ", pad)
		}
		emph := base.Bold(true).Underline(true)
		var b strings.Builder
		b.WriteString(base.Render(prefix + pre))
		if mid != "" {
			b.WriteString(emph.Render(mid))
		}
		b.WriteString(base.Render(suf + tailPad))
		return b.String()
	}

	delRow := build(styleDiffRemove, "- ",
		string(or[:p]), string(or[p:len(or)-s]), string(or[len(or)-s:]))
	addRow := build(styleDiffAdd, "+ ",
		string(nr[:p]), string(nr[p:len(nr)-s]), string(nr[len(nr)-s:]))
	return delRow, addRow, true
}

// splitDiffLines splits into lines, dropping a single trailing newline, and
// returns nil for empty input (so pure insertions/deletions have no lines).
func splitDiffLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// truncCell truncates s to display width w (ellipsis when cut), expanding tabs
// but preserving leading indentation — unlike compactOneLine, which collapses
// whitespace and would flatten code structure in diffs.
func truncCell(s string, w int) string {
	s = strings.ReplaceAll(s, "\t", "    ")
	if w < 1 {
		w = 1
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > w {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func lastN(s []string, n int) []string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// str coerces a JSON value to a string (empty for non-strings/nil).
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// renderToolBody renders the truncated result: dim lines behind a rail,
// expanded by ctrl+o (m.showToolOutput).
// Deprecated: use renderGenericToolBody or the per-type renderers.
func (m *model) renderToolBody(l *protocol.TranscriptLine, w int) string {
	return m.renderGenericToolBody(l, w)
}
