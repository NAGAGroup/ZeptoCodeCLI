package list

import (
	"fmt"
	"strings"
	"testing"
)

type fakeItem struct {
	id    int
	lines int
}

func (f *fakeItem) Render(width int) string {
	out := make([]string, f.lines)
	for i := range out {
		out[i] = fmt.Sprintf("item%d-line%d", f.id, i)
	}
	return strings.Join(out, "\n")
}

func (f *fakeItem) CacheKey() string { return fmt.Sprintf("%d", f.lines) }

func mkList(t *testing.T, heights ...int) *List {
	t.Helper()
	l := New()
	for i, h := range heights {
		l.Append(&fakeItem{id: i, lines: h})
	}
	l.SetSize(40, 10)
	return l
}

func TestFollowShowsBottom(t *testing.T) {
	l := mkList(t, 5, 5, 5) // total 15 + 2 gap = 17 lines, window 10
	v := l.View()
	lines := strings.Split(v, "\n")
	if len(lines) != 10 {
		t.Fatalf("want 10 lines, got %d", len(lines))
	}
	if lines[9] != "item2-line4" {
		t.Fatalf("bottom line = %q, want item2-line4", lines[9])
	}
}

func TestScrollUpAndBackDown(t *testing.T) {
	l := mkList(t, 5, 5, 5)
	l.ScrollBy(-4)
	if l.Follow() {
		t.Fatal("scroll up should release follow")
	}
	v := strings.Split(l.View(), "\n")
	if v[len(v)-1] == "item2-line4" {
		t.Fatal("window should have moved up")
	}
	l.ScrollBy(100) // past the end
	if !l.Follow() {
		t.Fatal("scrolling past end should re-engage follow")
	}
}

func TestGotoTop(t *testing.T) {
	l := mkList(t, 5, 5, 5)
	l.GotoTop()
	v := strings.Split(l.View(), "\n")
	if v[0] != "item0-line0" {
		t.Fatalf("top line = %q, want item0-line0", v[0])
	}
}

func TestShortContentPadsWindow(t *testing.T) {
	l := mkList(t, 3) // 3 lines in a 10-line window
	v := strings.Split(l.View(), "\n")
	if len(v) != 10 {
		t.Fatalf("want padded 10 lines, got %d", len(v))
	}
	if v[0] != "item0-line0" {
		t.Fatalf("short content should start at top, got %q", v[0])
	}
}

func TestCacheInvalidationOnKeyChange(t *testing.T) {
	l := mkList(t, 3)
	_ = l.View()
	it := l.items[0].(*fakeItem)
	it.lines = 5 // CacheKey changes with lines
	v := strings.Split(l.View(), "\n")
	if v[4] != "item0-line4" {
		t.Fatalf("expected re-render after key change, got %q", v[4])
	}
}
