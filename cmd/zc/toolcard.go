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
	toolBodyCollapsedLines = 4
	toolBodyExpandedLines  = 30
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

// renderToolCard renders a tool_call transcript Line: header + optional body.
func (m *model) renderToolCard(l *protocol.TranscriptLine, w int) string {
	name := humanizeToolName(l.Name)
	params := compactOneLine(toolParams(l.Name, l.ArgsText, m.serverCWD), max(20, w-len(name)-8))

	var icon, suffix string
	nameStyle := styleToolName
	switch toolStatus(l) {
	case "success":
		icon = styleToolOK.Render(iconToolOK)
	case "error":
		icon = styleToolErr.Render(iconToolErr)
		suffix = "  " + styleToolErr.Render("error")
	default: // running / streaming args
		if m.isExecuting(l) {
			icon = styleAccent.Render("▸")
			suffix = "  " + styleAccent.Render("running")
		} else {
			icon = styleTool.Render(iconToolPending)
		}
	}
	header := fmt.Sprintf("%s %s %s%s", icon, nameStyle.Render(name), styleToolParam.Render(params), suffix)

	// Edit/Write/MultiEdit: render a colored diff from the args (native diffview).
	if diff := m.renderEditDiff(l, w); diff != "" {
		return header + "\n" + diff
	}
	body := m.renderToolBody(l, w)
	if body == "" {
		return header
	}
	return header + "\n" + body
}

// renderEditDiff builds a colored +/- diff for Edit/Write/MultiEdit tool cards
// from the tool arguments (old_string→new_string, or content for Write).
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
	case "Edit":
		edits = append(edits, edit{str(args["old_string"]), str(args["new_string"])})
	case "MultiEdit":
		if raw, ok := args["edits"].([]any); ok {
			for _, e := range raw {
				if em, ok := e.(map[string]any); ok {
					edits = append(edits, edit{str(em["old_string"]), str(em["new_string"])})
				}
			}
		}
	case "Write":
		edits = append(edits, edit{"", str(args["content"])})
	default:
		return ""
	}
	maxLines := toolBodyCollapsedLines
	if m.showToolOutput {
		maxLines = toolBodyExpandedLines
	}
	bodyW := max(10, w-4)
	var out []string
	for _, e := range edits {
		if e.oldS != "" {
			for _, ln := range strings.Split(strings.TrimRight(e.oldS, "\n"), "\n") {
				out = append(out, styleDiffRemove.Render("  │ - "+compactOneLine(ln, bodyW)))
			}
		}
		if e.newS != "" {
			for _, ln := range strings.Split(strings.TrimRight(e.newS, "\n"), "\n") {
				out = append(out, styleDiffAdd.Render("  │ + "+compactOneLine(ln, bodyW)))
			}
		}
	}
	if len(out) == 0 {
		return ""
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
		res += "\n" + styleToolBody.Render(fmt.Sprintf("  │ … +%d lines (%s)", n, hint))
	}
	return res
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
func (m *model) renderToolBody(l *protocol.TranscriptLine, w int) string {
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
		b.WriteString(styleToolBody.Render("  │ "+ln) + "\n")
	}
	if n := len(lines) - len(shown); n > 0 {
		hint := "ctrl+o expands"
		if m.showToolOutput {
			hint = "output capped"
		}
		b.WriteString(styleToolBody.Render(fmt.Sprintf("  │ … +%d lines (%s)", n, hint)) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
