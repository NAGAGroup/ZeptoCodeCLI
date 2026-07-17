// Package list is a lazily-rendered scrollable transcript list, designed
// after the architecture of crush's internal/ui/list (charmbracelet/crush,
// FSL-1.1-MIT) but implemented fresh for zc's needs: items render on
// demand, rendered output is memoized per item keyed by an
// invalidation string, and only the visible window is materialized per
// frame — a full-transcript re-join never happens.
//
// Scroll state is (offsetIdx, offsetLine): the first visible item and how
// many of its lines are scrolled above the window. A follow flag keeps the
// view pinned to the bottom while streaming, and any upward scroll
// releases it.
//
// Frozen items: items whose CacheKey() returns "" are considered frozen
// (never re-rendered). Their memoized lines are kept forever and their
// height is not recomputed. This allows finished transcript items to be
// cached permanently while only live items re-render.
package list

import "strings"

// Item is one transcript entry.
type Item interface {
	// Render returns the item's block at the given width (no trailing
	// newline). It may be called lazily and repeatedly — cache internally
	// if expensive.
	Render(width int) string
	// CacheKey is the invalidation key: when it changes (or the width
	// does), the memoized render and height for this item are dropped.
	// If CacheKey returns "" the item is considered FROZEN: its render
	// is memoized forever and never recomputed.
	CacheKey() string
}

type cacheEnt struct {
	width int
	key   string
	lines []string
	frozen bool // never re-rendered
}

// List renders a vertical stack of items into a fixed-size window.
type List struct {
	items  []Item
	width  int
	height int
	gap    int // blank lines between items

	offsetIdx  int
	offsetLine int
	follow     bool

	cache map[Item]*cacheEnt
}

// New creates an empty list pinned to the bottom (follow mode).
func New() *List {
	return &List{follow: true, gap: 1, cache: map[Item]*cacheEnt{}}
}

// SetSize sets the window size. Width changes drop the render cache for
// non-frozen items.
func (l *List) SetSize(width, height int) {
	if width != l.width {
		for it, ent := range l.cache {
			if !ent.frozen {
				delete(l.cache, it)
			}
		}
	}
	l.width = width
	l.height = height
	l.clamp()
}

// SetItems replaces the item set (history replay, conversation switch).
func (l *List) SetItems(items []Item) {
	l.items = items
	l.cache = map[Item]*cacheEnt{}
	l.follow = true
	l.clamp()
}

// Append adds one item to the end.
func (l *List) Append(it Item) {
	l.items = append(l.items, it)
}

// Sync replaces the item set while PRESERVING the render cache and scroll
// state. This is the reducer path for the `transcript` frame, which pushes
// the whole Line[] every time: callers reuse stable Item identities (keyed by
// line id) so unchanged items keep their memoized render, and scroll/follow is
// not disturbed mid-stream. Cache entries for removed items are pruned.
func (l *List) Sync(items []Item) {
	present := make(map[Item]bool, len(items))
	for _, it := range items {
		present[it] = true
	}
	for it := range l.cache {
		if !present[it] {
			delete(l.cache, it)
		}
	}
	l.items = items
	l.clamp()
}

// Len returns the number of items.
func (l *List) Len() int { return len(l.items) }

// lines returns the memoized rendered lines for item i.
func (l *List) lines(i int) []string {
	it := l.items[i]
	key := it.CacheKey()
	if ent, ok := l.cache[it]; ok && ent.width == l.width {
		if ent.frozen || ent.key == key {
			return ent.lines
		}
	}
	rendered := it.Render(l.width)
	lines := strings.Split(rendered, "\n")
	l.cache[it] = &cacheEnt{width: l.width, key: key, lines: lines, frozen: key == ""}
	return lines
}

// itemHeight is the height of item i plus the trailing gap (except last).
func (l *List) itemHeight(i int) int {
	h := len(l.lines(i))
	if i < len(l.items)-1 {
		h += l.gap
	}
	return h
}

// totalHeight sums all item heights. O(n) but each term is memoized.
func (l *List) totalHeight() int {
	total := 0
	for i := range l.items {
		total += l.itemHeight(i)
	}
	return total
}

// AtBottom reports whether the window is at (or past) the end.
func (l *List) AtBottom() bool {
	if l.follow {
		return true
	}
	// Walk from the offset: if remaining lines fit in the window, we're
	// at the bottom.
	remaining := -l.offsetLine
	for i := l.offsetIdx; i < len(l.items); i++ {
		remaining += l.itemHeight(i)
		if remaining > l.height {
			return false
		}
	}
	return true
}

// Follow reports whether the list is pinned to the bottom.
func (l *List) Follow() bool { return l.follow }

// GotoBottom pins the window to the end (follow mode).
func (l *List) GotoBottom() { l.follow = true }

// GotoTop scrolls to the very beginning.
func (l *List) GotoTop() {
	l.follow = false
	l.offsetIdx, l.offsetLine = 0, 0
}

// ScrollBy scrolls by n lines (negative = up). Scrolling up releases
// follow; scrolling down past the end re-engages it.
func (l *List) ScrollBy(n int) {
	if l.follow {
		if n >= 0 {
			return
		}
		// Materialize the current top offset, then scroll from there.
		l.offsetIdx, l.offsetLine = l.bottomOffset()
		l.follow = false
	}
	l.offsetLine += n
	l.normalize()
	if l.AtBottom() {
		l.follow = true
	}
}

// PageUp / PageDown scroll by one window.
func (l *List) PageUp()   { l.ScrollBy(-l.height) }
func (l *List) PageDown() { l.ScrollBy(l.height) }
func (l *List) HalfUp()   { l.ScrollBy(-l.height / 2) }
func (l *List) HalfDown() { l.ScrollBy(l.height / 2) }

// bottomOffset computes the (offsetIdx, offsetLine) that shows the last
// window of content.
func (l *List) bottomOffset() (int, int) {
	need := l.height
	for i := len(l.items) - 1; i >= 0; i-- {
		h := l.itemHeight(i)
		if h >= need {
			return i, h - need
		}
		need -= h
	}
	return 0, 0
}

// normalize folds offsetLine over/underflow into offsetIdx and clamps.
func (l *List) normalize() {
	for l.offsetLine < 0 && l.offsetIdx > 0 {
		l.offsetIdx--
		l.offsetLine += l.itemHeight(l.offsetIdx)
	}
	if l.offsetLine < 0 {
		l.offsetLine = 0
	}
	for l.offsetIdx < len(l.items) && l.offsetLine >= l.itemHeight(l.offsetIdx) {
		l.offsetLine -= l.itemHeight(l.offsetIdx)
		l.offsetIdx++
	}
	l.clamp()
}

// clamp keeps the offset inside content bounds (no overscroll past end).
func (l *List) clamp() {
	if len(l.items) == 0 {
		l.offsetIdx, l.offsetLine = 0, 0
		return
	}
	if l.offsetIdx >= len(l.items) {
		l.offsetIdx = len(l.items) - 1
		l.offsetLine = 0
	}
	// Don't allow a window that starts past the bottom offset.
	bi, bl := l.bottomOffset()
	if l.offsetIdx > bi || (l.offsetIdx == bi && l.offsetLine > bl) {
		l.offsetIdx, l.offsetLine = bi, bl
	}
}

// View renders exactly height lines of the window.
func (l *List) View() string {
	if l.height <= 0 || len(l.items) == 0 {
		return strings.TrimRight(strings.Repeat("\n", max(0, l.height-1)), "")
	}
	startIdx, startLine := l.offsetIdx, l.offsetLine
	if l.follow {
		startIdx, startLine = l.bottomOffset()
	}

	out := make([]string, 0, l.height)
	skip := startLine
	for i := startIdx; i < len(l.items) && len(out) < l.height; i++ {
		lines := l.lines(i)
		for _, ln := range lines {
			if skip > 0 {
				skip--
				continue
			}
			out = append(out, ln)
			if len(out) >= l.height {
				break
			}
		}
		if i < len(l.items)-1 {
			for g := 0; g < l.gap && len(out) < l.height; g++ {
				if skip > 0 {
					skip--
					continue
				}
				out = append(out, "")
			}
		}
	}
	for len(out) < l.height {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
