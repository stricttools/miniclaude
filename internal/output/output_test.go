package output

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func filled(n int) *Buffer {
	b := New()
	for i := 0; i < n; i++ {
		b.Print(fmt.Sprintf("line %d\n", i))
	}
	return b
}

func TestFollowsTheTail(t *testing.T) {
	b := filled(20)
	b.Layout(40, 5)
	v := b.Visible()
	if len(v) != 5 || v[4] != "line 19" || !b.Following() {
		t.Fatalf("%q", v)
	}
	b.Print("open line without newline")
	b.Layout(40, 5)
	if v := b.Visible(); v[4] != "open line without newline" {
		t.Fatalf("the open line is not shown: %q", v)
	}
}

func TestWrapsAndCarriesColorAcrossRows(t *testing.T) {
	b := New()
	b.Print("\x1b[31m" + strings.Repeat("r", 15) + "\x1b[0m plain\n")
	b.Layout(10, 10)
	v := b.Visible()
	var rows []string
	for _, r := range v {
		if r != "" {
			rows = append(rows, r)
		}
	}
	if len(rows) != 3 || !strings.HasPrefix(rows[1], "\x1b[31m") {
		t.Fatalf("%q", rows)
	}
	for _, r := range rows {
		if Width(r) > 10 {
			t.Fatalf("row %q is wider than 10", r)
		}
	}
}

func TestWheelScrollingCoalescesAndReleasesTheLock(t *testing.T) {
	b := filled(30)
	b.Layout(40, 5)
	now := time.Unix(1000, 0)
	b.WheelUp(now)
	if b.Following() || b.Visible()[4] != "line 28" {
		t.Fatalf("the first event did not move one row: %q", b.Visible())
	}
	for i := 1; i < ScrollDivisor; i++ {
		b.WheelUp(now.Add(time.Duration(i) * time.Millisecond))
	}
	if b.Visible()[4] != "line 28" {
		t.Fatalf("events inside a burst moved the view: %q", b.Visible())
	}
	b.WheelUp(now.Add(20 * time.Millisecond))
	if b.Visible()[4] != "line 27" {
		t.Fatalf("the tenth event did not move: %q", b.Visible())
	}
	later := now.Add(time.Second)
	b.WheelDown(later)
	b.WheelDown(later.Add(time.Second))
	if !b.Following() || b.Visible()[4] != "line 29" {
		t.Fatalf("reaching the bottom did not release the lock: %q %v", b.Visible(), b.Following())
	}
	b.WheelDown(later.Add(2 * time.Second))
	if _, bottom := b.Hints(later.Add(2 * time.Second)); !bottom {
		t.Fatal("no bottom hint at the bottom")
	}
	if _, bottom := b.Hints(later.Add(2*time.Second + HintDuration)); bottom {
		t.Fatal("the hint did not expire")
	}
}

func TestScrollingAtTheTopFlashesTheTopHint(t *testing.T) {
	b := filled(3)
	b.Layout(40, 5)
	now := time.Unix(1000, 0)
	b.WheelUp(now)
	if top, _ := b.Hints(now); !top {
		t.Fatal("no top hint")
	}
	if !b.HintExpiry().Equal(now.Add(HintDuration)) {
		t.Fatalf("expiry %v", b.HintExpiry())
	}
}

func TestTablesAreRenderedAtTheLiveWidth(t *testing.T) {
	b := New()
	b.Print("before")
	b.AddTable(func(width int) []string { return []string{strings.Repeat("=", width)} })
	b.Print("after\n")
	b.Layout(12, 10)
	v := strings.Join(b.Visible(), "|")
	if !strings.Contains(v, "before|============|after") {
		t.Fatalf("%q", v)
	}
	b.Layout(6, 10)
	if v := strings.Join(b.Visible(), "|"); !strings.Contains(v, "======|after") {
		t.Fatalf("after a resize: %q", v)
	}
}

func TestTruncateAndControlCharacters(t *testing.T) {
	if got := Truncate("\x1b[1mbold text\x1b[0m", 4); Width(got) != 4 {
		t.Fatalf("%q", got)
	}
	if rows := Wrap("a\tb", 10); rows[0] != "a^Ib" {
		t.Fatalf("%q", rows)
	}
	if rows := Wrap("日本語", 3); len(rows) != 3 {
		t.Fatalf("wide characters: %q", rows)
	}
}
