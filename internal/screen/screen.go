// Package screen draws the fullscreen REPL's frame: the output rows at the
// top, a box drawn with ┌─┐│└┘ holding the input editor or a modal, and the
// status rows at the bottom. The box sits right above the status rows and
// the output takes the rows left above it. A boundary hint is a
// reverse-video yellow rule over the top or bottom output row. Only rows
// that changed since the last draw are rewritten.
package screen

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/stricttools/miniclaude/internal/output"
)

// StatusRows is the number of status rows under the box.
const StatusRows = 3

// hintStyle styles the boundary hint: reverse video, yellow.
const hintStyle = "\x1b[7;33m"

const reset = "\x1b[0m"

// Frame is everything one draw shows.
type Frame struct {
	// Output holds the output rows from the top of the output area; rows
	// past its end are blank.
	Output []string
	// TopHint and BottomHint draw the boundary hint over the first and the
	// last row of the output area.
	TopHint    bool
	BottomHint bool
	// Box holds the lines inside the box: the input editor's rows, or a
	// modal's lines in place of them. It needs at least one line.
	Box []string
	// ShowCursor shows the cursor at CursorRow and CursorCol, counted in
	// cells from the first cell inside the box.
	ShowCursor bool
	CursorRow  int
	CursorCol  int
	// Status holds the status rows; lines past StatusRows are not shown.
	Status []string
}

// OutputHeight returns how many rows the output area gets on a screen of
// rows rows when the box holds boxLines lines.
func OutputHeight(rows, boxLines int) int {
	return max(0, rows-(boxLines+2)-StatusRows)
}

// Screen draws frames to a terminal and remembers the last one drawn.
type Screen struct {
	w    io.Writer
	prev []string
	cols int
	rows int
	buf  bytes.Buffer
}

// New returns a screen drawing to w, which must be the terminal set up by
// the term package; the first draw repaints every row.
func New(w io.Writer) *Screen {
	return &Screen{w: w}
}

// Invalidate makes the next draw clear the screen and repaint every row.
func (s *Screen) Invalidate() {
	s.prev = nil
}

// Draw draws f on a screen of cols columns and rows rows, rewriting the
// rows that differ from the last draw. A size change repaints everything.
// When the frame is taller than the screen, its top rows are cut. Writing
// to the terminal is the one blocking call; when it fails the next draw
// repaints everything.
func (s *Screen) Draw(f Frame, cols, rows int) error {
	if cols < 1 || rows < 1 {
		return nil
	}
	lines := compose(f, cols, rows)
	cut := len(lines) - rows
	lines = lines[cut:]

	full := s.prev == nil || cols != s.cols || rows != s.rows
	s.buf.Reset()
	s.buf.WriteString("\x1b[?2026h\x1b[?25l")
	if full {
		s.buf.WriteString("\x1b[0m\x1b[2J")
	}
	for i, line := range lines {
		if !full && i < len(s.prev) && s.prev[i] == line {
			continue
		}
		fmt.Fprintf(&s.buf, "\x1b[%d;1H\x1b[2K%s", i+1, line)
	}
	boxTop := OutputHeight(rows, len(f.Box)) - cut
	cursorRow := boxTop + 1 + f.CursorRow
	if f.ShowCursor && cursorRow >= 0 && cursorRow < rows {
		cursorCol := min(1+f.CursorCol, cols-1)
		fmt.Fprintf(&s.buf, "\x1b[%d;%dH\x1b[?25h", cursorRow+1, cursorCol+1)
	}
	s.buf.WriteString("\x1b[?2026l")

	s.cols, s.rows = cols, rows
	if _, err := s.w.Write(s.buf.Bytes()); err != nil {
		s.prev = nil
		return fmt.Errorf("screen: drawing: %w", err)
	}
	s.prev = lines
	return nil
}

// compose returns every row of f, top to bottom, each cut to cols cells.
// It returns at least rows rows.
func compose(f Frame, cols, rows int) []string {
	inner := max(0, cols-2)
	outHeight := OutputHeight(rows, len(f.Box))
	lines := make([]string, 0, outHeight+len(f.Box)+2+StatusRows)

	for i := range outHeight {
		row := ""
		if i < len(f.Output) {
			row = f.Output[i]
		}
		lines = append(lines, output.Truncate(row, cols))
	}
	if outHeight > 0 {
		rule := hintStyle + strings.Repeat("─", cols) + reset
		if f.TopHint {
			lines[0] = rule
		}
		if f.BottomHint {
			lines[outHeight-1] = rule
		}
	}

	lines = append(lines, output.Truncate("┌"+strings.Repeat("─", inner)+"┐", cols))
	for _, line := range f.Box {
		content := output.Truncate(line, inner)
		pad := strings.Repeat(" ", max(0, inner-output.Width(content)))
		lines = append(lines, output.Truncate("│"+content+pad+"│", cols))
	}
	lines = append(lines, output.Truncate("└"+strings.Repeat("─", inner)+"┘", cols))

	for i := range StatusRows {
		row := ""
		if i < len(f.Status) {
			row = f.Status[i]
		}
		lines = append(lines, output.Truncate(row, cols))
	}
	return lines
}
