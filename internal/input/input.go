// Package input is the REPL's multiline line editor: the text in the input
// frame, its cursor, history recall with Up and Down, and the suggestion of
// the latest history line that starts with what the current line holds
// (Right or Ctrl+E at the end of the text accepts it). The editing keys
// follow prompt_toolkit's Emacs bindings, which the Python REPL used.
//
// Editor does no I/O. Recording a submitted line in the history file is the
// caller's decision: Submit reports whether the line is a new history entry.
package input

import (
	"strings"
	"unicode"

	"github.com/stricttools/miniclaude/internal/keys"
	"github.com/stricttools/miniclaude/internal/output"
)

// MaxRows is the most rows the input frame shows; longer text scrolls
// inside it to keep the cursor in view.
const MaxRows = 10

// suggestionStyle draws the suggestion in prompt_toolkit's auto-suggestion
// gray (#666666).
const suggestionStyle = "\x1b[38;2;102;102;102m"

const reset = "\x1b[0m"

// Editor is the input frame's text and history state.
type Editor struct {
	text   []rune
	cursor int

	// history holds the entries oldest first. working holds one string per
	// entry plus the line being typed; recalling an entry and editing it
	// changes working, never history, until the next Reset. index is the
	// working position being edited.
	history []string
	working []string
	index   int

	// suggestion is the suggestion computed after the last insertion. Like
	// prompt_toolkit's, it is computed only when text is inserted and is
	// cleared by every other change of the text.
	suggestion string

	// scroll is the first wrapped row the frame shows.
	scroll int
}

// New returns an empty editor over history, oldest entry first.
func New(history []string) *Editor {
	e := &Editor{history: append([]string(nil), history...)}
	e.Reset()
	return e
}

// Text returns the text being edited.
func (e *Editor) Text() string {
	return string(e.text)
}

// Reset clears the text and forgets edits made to recalled entries.
func (e *Editor) Reset() {
	e.text = nil
	e.cursor = 0
	e.scroll = 0
	e.suggestion = ""
	e.working = append(append([]string(nil), e.history...), "")
	e.index = len(e.working) - 1
}

// Submission is a submitted line.
type Submission struct {
	Text string
	// NewHistoryEntry reports that Text was added to the in-memory history
	// and belongs in the history file; it is false when Text repeats the
	// latest entry.
	NewHistoryEntry bool
}

// Submit takes the text for submission and clears the editor. Text that is
// empty or only whitespace is cleared and not submitted: the second result
// is false.
func (e *Editor) Submit() (Submission, bool) {
	text := string(e.text)
	if strings.TrimSpace(text) == "" {
		e.Reset()
		return Submission{}, false
	}
	isNew := len(e.history) == 0 || e.history[len(e.history)-1] != text
	if isNew {
		e.history = append(e.history, text)
	}
	e.Reset()
	return Submission{Text: text, NewHistoryEntry: isNew}, true
}

// Apply performs an editing key and reports whether it was one. Enter,
// Escape, Ctrl+C, Ctrl+D, and the wheel are not editing keys; the caller
// decides what they do.
func (e *Editor) Apply(ev keys.Event) bool {
	switch ev.Key {
	case keys.KeyRune:
		e.insert([]rune{ev.Rune})
	case keys.KeyAltEnter:
		e.insert([]rune{'\n'})
	case keys.KeyPaste:
		text := strings.ReplaceAll(ev.Text, "\r\n", "\n")
		e.insert([]rune(strings.ReplaceAll(text, "\r", "\n")))
	case keys.KeyBackspace:
		if e.cursor > 0 {
			e.delete(e.cursor-1, e.cursor)
		}
	case keys.KeyDelete:
		if e.cursor < len(e.text) {
			e.delete(e.cursor, e.cursor+1)
		}
	case keys.KeyLeft:
		if e.cursor > 0 && e.text[e.cursor-1] != '\n' {
			e.cursor--
		}
	case keys.KeyRight:
		if !e.acceptSuggestion() && e.cursor < len(e.text) && e.text[e.cursor] != '\n' {
			e.cursor++
		}
	case keys.KeyCtrlE:
		if !e.acceptSuggestion() {
			e.cursor = e.lineEnd(e.cursor)
		}
	case keys.KeyCtrlA, keys.KeyHome:
		e.cursor = e.lineStart(e.cursor)
	case keys.KeyEnd:
		e.cursor = e.lineEnd(e.cursor)
	case keys.KeyUp:
		e.up()
	case keys.KeyDown:
		e.down()
	case keys.KeyCtrlK:
		if e.cursor < len(e.text) && e.text[e.cursor] == '\n' {
			e.delete(e.cursor, e.cursor+1)
		} else {
			e.delete(e.cursor, e.lineEnd(e.cursor))
		}
	case keys.KeyCtrlU:
		start := e.lineStart(e.cursor)
		if start == e.cursor && e.cursor > 0 {
			e.delete(e.cursor-1, e.cursor)
		} else {
			e.delete(start, e.cursor)
		}
	case keys.KeyCtrlW:
		e.delete(e.previousWordStart(), e.cursor)
	default:
		return false
	}
	return true
}

// insert inserts runes at the cursor and moves the cursor past them.
func (e *Editor) insert(runes []rune) {
	text := make([]rune, 0, len(e.text)+len(runes))
	text = append(text, e.text[:e.cursor]...)
	text = append(text, runes...)
	e.text = append(text, e.text[e.cursor:]...)
	e.cursor += len(runes)
	e.suggestion = e.suggest()
}

// delete removes the runes in [from, to) and leaves the cursor at from.
func (e *Editor) delete(from, to int) {
	if from == to {
		return
	}
	e.text = append(e.text[:from], e.text[to:]...)
	e.cursor = from
	e.suggestion = ""
}

// lineStart returns the index where the line holding i starts.
func (e *Editor) lineStart(i int) int {
	for i > 0 && e.text[i-1] != '\n' {
		i--
	}
	return i
}

// lineEnd returns the index of the newline ending the line holding i, or
// the text's length on the last line.
func (e *Editor) lineEnd(i int) int {
	for i < len(e.text) && e.text[i] != '\n' {
		i++
	}
	return i
}

// previousWordStart returns where Ctrl+W deletes back to: the start of the
// whitespace-delimited word before the cursor, the whitespace after it
// included, or the start of the text when only whitespace precedes the
// cursor.
func (e *Editor) previousWordStart() int {
	i := e.cursor
	for i > 0 && unicode.IsSpace(e.text[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(e.text[i-1]) {
		i--
	}
	return i
}

// up moves the cursor to the line above, or recalls the previous history
// entry from the first line, with the cursor at the end of the entry.
func (e *Editor) up() {
	start := e.lineStart(e.cursor)
	if start > 0 {
		e.moveToLine(e.lineStart(start-1), e.cursor-start)
		return
	}
	if e.index == 0 {
		return
	}
	e.recall(e.index - 1)
	e.cursor = len(e.text)
}

// down moves the cursor to the line below, or recalls the next history
// entry from the last line, with the cursor at the end of its first line.
func (e *Editor) down() {
	end := e.lineEnd(e.cursor)
	if end < len(e.text) {
		e.moveToLine(end+1, e.cursor-e.lineStart(e.cursor))
		return
	}
	if e.index == len(e.working)-1 {
		return
	}
	e.recall(e.index + 1)
	e.cursor = e.lineEnd(0)
}

// moveToLine puts the cursor at column col of the line starting at start,
// or at that line's end when it is shorter.
func (e *Editor) moveToLine(start, col int) {
	e.cursor = min(start+col, e.lineEnd(start))
}

// recall keeps the current text as the working copy of its position and
// loads the working copy at index.
func (e *Editor) recall(index int) {
	e.working[e.index] = string(e.text)
	e.index = index
	e.text = []rune(e.working[index])
	e.scroll = 0
	e.suggestion = ""
}

// Suggestion returns the text accepting the suggestion would add, or ""
// when there is none or the cursor is not at the end of the text.
func (e *Editor) Suggestion() string {
	if e.cursor != len(e.text) {
		return ""
	}
	return e.suggestion
}

// suggest returns the rest of the latest history line that starts with the
// text's last line, when that last line is not blank. Entries are searched
// newest first, the lines of each entry last first.
func (e *Editor) suggest() string {
	last := string(e.text[e.lineStart(len(e.text)):])
	if strings.TrimSpace(last) == "" {
		return ""
	}
	for i := len(e.history) - 1; i >= 0; i-- {
		lines := strings.Split(e.history[i], "\n")
		for j := len(lines) - 1; j >= 0; j-- {
			if strings.HasPrefix(lines[j], last) {
				return lines[j][len(last):]
			}
		}
	}
	return ""
}

// acceptSuggestion inserts the suggestion and reports whether there was one.
func (e *Editor) acceptSuggestion() bool {
	s := e.Suggestion()
	if s == "" {
		return false
	}
	e.insert([]rune(s))
	return true
}

// View is the input frame's content at a width.
type View struct {
	// Rows are the visible rows, at most MaxRows, each at most the width
	// passed to Render. There is always at least one row.
	Rows []string
	// CursorRow and CursorCol place the cursor within Rows, in cells.
	CursorRow int
	CursorCol int
}

// Render lays the text out in rows of at most width cells, hard-wrapping
// long lines, with the suggestion in gray after the text, and scrolls the
// rows so the cursor stays among the MaxRows shown. A cursor that would sit
// past the last cell of a full row is placed at the start of the next row.
func (e *Editor) Render(width int) View {
	if width < 1 {
		width = 1
	}
	var rows []string
	var row strings.Builder
	col := 0
	gray := false
	cursorRow, cursorCol := 0, 0
	endRow := func() {
		if gray {
			row.WriteString(reset)
		}
		rows = append(rows, row.String())
		row.Reset()
		if gray {
			row.WriteString(suggestionStyle)
		}
		col = 0
	}
	place := func(r rune) {
		disp, w := output.DisplayRune(r)
		if col > 0 && col+w > width {
			endRow()
		}
		row.WriteString(disp)
		col += w
	}
	markCursorAtEnd := func() {
		if col >= width {
			endRow()
		}
		cursorRow, cursorCol = len(rows), col
	}
	for i, r := range e.text {
		if r == '\n' {
			if i == e.cursor {
				markCursorAtEnd()
			}
			endRow()
			continue
		}
		if i == e.cursor {
			_, w := output.DisplayRune(r)
			if col > 0 && col+w > width {
				endRow()
			}
			cursorRow, cursorCol = len(rows), col
		}
		place(r)
	}
	if e.cursor == len(e.text) {
		markCursorAtEnd()
	}
	if s := e.Suggestion(); s != "" {
		gray = true
		row.WriteString(suggestionStyle)
		for _, r := range s {
			place(r)
		}
		row.WriteString(reset)
		gray = false
	}
	rows = append(rows, row.String())

	if len(rows) <= MaxRows {
		e.scroll = 0
	} else {
		if cursorRow < e.scroll {
			e.scroll = cursorRow
		}
		if cursorRow >= e.scroll+MaxRows {
			e.scroll = cursorRow - MaxRows + 1
		}
		e.scroll = min(e.scroll, len(rows)-MaxRows)
	}
	shown := rows[e.scroll:min(len(rows), e.scroll+MaxRows)]
	return View{Rows: shown, CursorRow: cursorRow - e.scroll, CursorCol: cursorCol}
}
