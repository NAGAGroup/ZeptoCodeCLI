// Streaming markdown with a stable-prefix cache, after crush's
// streaming_markdown.go (design, not code): during streaming, the portion
// of the document before a provably safe boundary is rendered once and
// memoized; each frame only re-renders the tail. Deliberately
// conservative — any doubt falls back to a full render.
package main

import "strings"

type streamMD struct {
	width          int
	prefix         string // literal byte prefix of the source content
	prefixRendered string // glamour render of prefix, whitespace-trimmed
}

func (s *streamMD) reset() {
	s.width = 0
	s.prefix = ""
	s.prefixRendered = ""
}

// safeBoundary returns a byte offset ending at a blank line such that the
// prefix before it contains no open fenced code block, or -1. It only
// considers boundaries at least 400 bytes before the end so the boundary
// itself is stable against in-flight paragraph growth.
func safeBoundary(content string) int {
	limit := len(content) - 400
	if limit <= 0 {
		return -1
	}
	idx := strings.LastIndex(content[:limit], "\n\n")
	if idx < 0 {
		return -1
	}
	cut := idx + 2
	prefix := content[:cut]
	// Open fence anywhere before the boundary ⇒ unsafe.
	if strings.Count(prefix, "```")%2 != 0 {
		return -1
	}
	// Tables and setext headers depend on adjacent lines; if the boundary
	// area looks like either, refuse.
	tail := prefix
	if len(tail) > 200 {
		tail = tail[len(tail)-200:]
	}
	if strings.Contains(tail, "|") || strings.Contains(tail, "===") || strings.Contains(tail, "---") {
		return -1
	}
	return cut
}

// render renders content, reusing the cached stable-prefix render when the
// content still extends the cached prefix at the same width.
func (m *model) renderStreamingMarkdown(s *streamMD, content string, width int) string {
	r := m.markdownRenderer(width)
	full := func() string {
		if r == nil {
			return content
		}
		out, err := r.Render(content)
		if err != nil {
			return content
		}
		return strings.Trim(out, "\n")
	}

	if len(content) < 1500 {
		return full()
	}
	if s.width != width || (s.prefix != "" && !strings.HasPrefix(content, s.prefix)) {
		s.reset()
		s.width = width
	}
	if s.prefix == "" {
		b := safeBoundary(content)
		if b < 0 {
			return full()
		}
		if r == nil {
			return content
		}
		rendered, err := r.Render(content[:b])
		if err != nil {
			return full()
		}
		s.prefix = content[:b]
		s.prefixRendered = strings.Trim(rendered, "\n")
	}
	if r == nil {
		return content
	}
	tail, err := r.Render(content[len(s.prefix):])
	if err != nil {
		return full()
	}
	// Try to advance the boundary for the next frame.
	if b := safeBoundary(content); b > len(s.prefix) {
		if rendered, err := r.Render(content[:b]); err == nil {
			s.prefix = content[:b]
			s.prefixRendered = strings.Trim(rendered, "\n")
		}
	}
	return s.prefixRendered + "\n\n" + strings.Trim(tail, "\n")
}
