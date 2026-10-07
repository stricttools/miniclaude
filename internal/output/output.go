// Package output holds the scrollable output region of the fullscreen REPL:
// prose printed as text with SGR escapes, and tables that re-materialize at
// the live width. Lines are hard-wrapped into rows with runewidth, every row
// re-establishing the SGR attributes in force where it starts. The view
// follows the tail until the user scrolls up with the mouse wheel; wheel
// events are coalesced (the first event of a burst moves one row at once,
// then every ScrollDivisor-th event moves one more), and a downward move
// that reaches the bottom releases the scroll lock. A wheel event blocked at
// either edge flashes that edge's boundary hint for HintDuration.
//
// Buffer does no I/O and never blocks; one goroutine owns it.
package output

import (
	"sort"
	"strings"
	"time"
)

// ScrollDivisor is how many wheel events of one burst it takes to move one
// row, after the burst's first event, which moves at once.
const ScrollDivisor = 10

// BurstGap ends a wheel burst: an event later than this after the previous
// one, or in the other direction, starts a new burst.
const BurstGap = 300 * time.Millisecond

// HintDuration is how long a boundary hint stays visible after a wheel
// event was blocked at an edge.
const HintDuration = 400 * time.Millisecond

// direction is the direction of the last wheel event.
type direction int

const (
	dirNone direction = iota
	dirUp
	dirDown
)

// entry is one unit of output: a complete prose line with the SGR state in
// force at its start, or a table.
type entry struct {
	line  string
	state sgrState
	table func(width int) []string
}

// Buffer is the output region's content and scroll state.
type Buffer struct {
	entries []entry

	// partial is the prose after the last newline, shown as the last rows
	// until its newline arrives. proseState is the SGR state at its start.
	partial    string
	proseState sgrState

	// rows are the wrapped rows of entries[:wrapped] at width; rowEntry[i]
	// is the index of the entry row i came from. tail holds the rows of
	// partial at width, rebuilt on every refresh.
	width    int
	height   int
	rows     []string
	rowEntry []int
	wrapped  int
	tail     []string

	// top is the index of the first visible row. While locked is false it
	// follows the tail.
	top             int
	locked          bool
	upCount         int
	downCount       int
	lastDir         direction
	lastWheel       time.Time
	topHintUntil    time.Time
	bottomHintUntil time.Time
}

// New returns an empty buffer. Nothing is laid out until Layout is called.
func New() *Buffer {
	return &Buffer{}
}

// Print appends prose. Consecutive prints continue one another: text after
// the last newline stays the open last line until a later print ends it.
// SGR attributes carry across lines as a terminal would carry them.
func (b *Buffer) Print(text string) {
	if text == "" {
		return
	}
	pieces := strings.Split(b.partial+text, "\n")
	for _, line := range pieces[:len(pieces)-1] {
		b.entries = append(b.entries, entry{line: line, state: b.proseState.clone()})
		b.proseState.advance(line)
	}
	b.partial = pieces[len(pieces)-1]
}

// AddTable appends a table. render materializes it at a width and is called
// again whenever the width changes. Open prose (text not yet ended by a
// newline) is ended first, so the table starts on a row of its own; the
// table's rows start with no SGR attributes and do not change the prose's.
func (b *Buffer) AddTable(render func(width int) []string) {
	if b.partial != "" {
		b.Print("\n")
	}
	b.entries = append(b.entries, entry{table: render})
}

// Layout sets the region's size. A width change re-wraps every row; while
// the view is scroll-locked, it keeps the entry at the top of the view at
// the top.
func (b *Buffer) Layout(width, height int) {
	if width < 1 {
		width = 1
	}
	if height < 0 {
		height = 0
	}
	if width != b.width && b.locked {
		anchor := b.entryAt(b.top)
		b.width = width
		b.height = height
		b.rows, b.rowEntry, b.wrapped = nil, nil, 0
		b.refresh()
		b.top = sort.SearchInts(b.rowEntry, anchor)
		b.clampTop()
		return
	}
	if width != b.width {
		b.rows, b.rowEntry, b.wrapped = nil, nil, 0
	}
	b.width = width
	b.height = height
	b.refresh()
}

// entryAt returns the index of the entry row i came from; rows of the open
// prose line belong to the index one past the last entry.
func (b *Buffer) entryAt(i int) int {
	if i < len(b.rowEntry) {
		return b.rowEntry[i]
	}
	return len(b.entries)
}

// refresh wraps the entries added since the last refresh, rebuilds the open
// line's rows, and moves the view to the tail unless it is locked. It does
// nothing before the first Layout.
func (b *Buffer) refresh() {
	if b.width == 0 {
		return
	}
	for ; b.wrapped < len(b.entries); b.wrapped++ {
		e := b.entries[b.wrapped]
		var rows []string
		if e.table != nil {
			for _, line := range e.table(b.width) {
				rows = append(rows, Wrap(line, b.width)...)
			}
		} else {
			st := e.state.clone()
			rows = wrapLine(e.line, b.width, &st)
		}
		for range rows {
			b.rowEntry = append(b.rowEntry, b.wrapped)
		}
		b.rows = append(b.rows, rows...)
	}
	b.tail = nil
	if b.partial != "" {
		st := b.proseState.clone()
		b.tail = wrapLine(b.partial, b.width, &st)
	}
	b.clampTop()
}

// rowCount returns the number of rows at the current width.
func (b *Buffer) rowCount() int {
	return len(b.rows) + len(b.tail)
}

// maxTop returns the top row index that shows the last row at the bottom.
func (b *Buffer) maxTop() int {
	return max(0, b.rowCount()-b.height)
}

// clampTop follows the tail when unlocked and keeps a locked view in range.
func (b *Buffer) clampTop() {
	if !b.locked {
		b.top = b.maxTop()
		return
	}
	b.top = min(max(b.top, 0), b.maxTop())
}

// Visible returns the rows in view, top to bottom: at most the height given
// to Layout, fewer when there is less output. Each row fits the width.
func (b *Buffer) Visible() []string {
	b.refresh()
	end := min(b.top+b.height, b.rowCount())
	visible := make([]string, 0, max(0, end-b.top))
	for i := b.top; i < end; i++ {
		if i < len(b.rows) {
			visible = append(visible, b.rows[i])
		} else {
			visible = append(visible, b.tail[i-len(b.rows)])
		}
	}
	return visible
}

// Following reports whether the view follows the tail.
func (b *Buffer) Following() bool {
	return !b.locked
}

// startsBurst reports whether a wheel event in dir at now starts a burst.
func (b *Buffer) startsBurst(dir direction, now time.Time) bool {
	return b.lastDir != dir || now.Sub(b.lastWheel) > BurstGap
}

// WheelUp handles one wheel-up event at now. Every wheel-up locks the view;
// the burst's first event and every ScrollDivisor-th after it move the view
// up one row. An event at the top flashes the top hint.
func (b *Buffer) WheelUp(now time.Time) {
	b.refresh()
	atTop := b.top <= 0
	newBurst := b.startsBurst(dirUp, now)
	if b.lastDir != dirUp {
		b.downCount = 0
	}
	b.lastDir = dirUp
	b.lastWheel = now
	b.locked = true
	if newBurst {
		b.upCount = 1
	} else {
		b.upCount++
	}
	if (b.upCount-1)%ScrollDivisor == 0 && b.top > 0 {
		b.top--
	}
	if atTop && b.top <= 0 {
		b.topHintUntil = now.Add(HintDuration)
	}
}

// WheelDown handles one wheel-down event at now. The burst's first event
// and every ScrollDivisor-th after it move the view down one row, and such
// a move ending at the bottom releases the lock so the view follows the
// tail again. An event at the bottom flashes the bottom hint.
func (b *Buffer) WheelDown(now time.Time) {
	b.refresh()
	atBottom := b.top >= b.maxTop()
	newBurst := b.startsBurst(dirDown, now)
	if b.lastDir != dirDown {
		b.upCount = 0
	}
	b.lastDir = dirDown
	b.lastWheel = now
	if newBurst {
		b.downCount = 1
	} else {
		b.downCount++
	}
	if (b.downCount-1)%ScrollDivisor == 0 {
		if b.top < b.maxTop() {
			b.top++
		}
		if b.top >= b.maxTop() {
			b.locked = false
		}
	}
	if atBottom && b.top >= b.maxTop() {
		b.bottomHintUntil = now.Add(HintDuration)
	}
}

// ResetScroll makes the view follow the tail again and forgets the wheel
// burst; the REPL calls it when input is submitted.
func (b *Buffer) ResetScroll() {
	b.locked = false
	b.upCount = 0
	b.downCount = 0
	b.lastDir = dirNone
	b.lastWheel = time.Time{}
	b.clampTop()
}

// Hints reports whether the top and bottom boundary hints show at now.
func (b *Buffer) Hints(now time.Time) (top, bottom bool) {
	return now.Before(b.topHintUntil), now.Before(b.bottomHintUntil)
}

// HintExpiry returns when the last boundary hint showing disappears; the
// controller redraws then to clear it. It is the zero time when no hint
// was ever shown.
func (b *Buffer) HintExpiry() time.Time {
	if b.topHintUntil.After(b.bottomHintUntil) {
		return b.topHintUntil
	}
	return b.bottomHintUntil
}
