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

// renderToolCard renders a tool entry: header + optional result body.
func (m *model) renderToolCard(e *entry, w int) string {
	name := humanizeToolName(e.toolName)
	params := compactOneLine(toolParams(e.toolName, e.toolArgs.String(), m.serverCWD), max(20, w-len(name)-8))

	var icon, suffix string
	var nameStyle lipgloss.Style
	switch e.toolStatus {
	case "success", "allowed":
		icon = styleToolOK.Render(iconToolOK)
		nameStyle = styleToolName
	case "error", "denied":
		icon = styleToolErr.Render(iconToolErr)
		nameStyle = styleToolName
		suffix = "  " + styleToolErr.Render(e.toolStatus)
	case "awaiting approval":
		icon = styleTool.Render(iconToolWait)
		nameStyle = styleToolName
		suffix = "  " + styleTool.Render("awaiting approval")
	default: // running / streaming args
		icon = styleTool.Render(iconToolPending)
		nameStyle = styleToolName
	}
	header := fmt.Sprintf("%s %s %s%s", icon, nameStyle.Render(name), styleToolParam.Render(params), suffix)

	body := m.renderToolBody(e, w)
	if body == "" {
		return header
	}
	return header + "\n" + body
}

// renderToolBody renders the truncated result: dim lines behind a rail,
// expanded by ctrl+o (m.showToolOutput).
func (m *model) renderToolBody(e *entry, w int) string {
	ret := strings.TrimRight(e.toolReturn, "\n")
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
