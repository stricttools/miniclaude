// Package render turns a streamed assistant reply into SGR-styled terminal
// text. It renders markdown prose (headers, bullets, inline code, bold,
// italic, links, and code fences) and thinking, shown dim gray under a
// "✻ thinking" header. Output is emitted in whole lines only: the renderer
// holds a partial trailing line until its newline arrives, so the output is
// the same however the stream is split into chunks. Table rows are held
// until the table ends and are handed back as a Table, rendered later at
// whatever width the terminal has then.
package render

import (
	"strings"

	"github.com/stricttools/miniclaude/internal/displaytext"
)

// Segment is one piece of rendered output, in order: either prose text
// (whole lines, each ending in a newline) or a table.
type Segment struct {
	// Text is the styled prose when Table is nil.
	Text string
	// Table is a finished table, nil for prose.
	Table *Table
}

type mode int

const (
	modeNone mode = iota
	modeText
	modeThinking
)

// Renderer renders one assistant reply. Its zero value is ready to use.
type Renderer struct {
	mode mode
	// buf holds the partial trailing line of the current mode.
	buf string
	// inCode is true inside a fenced code block of prose.
	inCode bool
	// table holds the raw rows of the table being read.
	table []string
	// out collects the output of the call in progress.
	out []Segment
}

// New returns a renderer for one assistant reply.
func New() *Renderer {
	return &Renderer{}
}

// FeedText feeds a chunk of prose and returns the output it completes.
func (r *Renderer) FeedText(chunk string) []Segment {
	if chunk == "" {
		return nil
	}
	if r.mode != modeText {
		r.switchToText()
	}
	r.buf += chunk
	r.drain()
	return r.take()
}

// FeedThinking feeds a chunk of thinking and returns the output it
// completes.
func (r *Renderer) FeedThinking(chunk string) []Segment {
	if chunk == "" {
		return nil
	}
	if r.mode != modeThinking {
		r.switchToThinking()
	}
	r.buf += chunk
	r.drain()
	return r.take()
}

// Finish flushes the held partial line and any open table. The mode and
// whether a code fence is open are kept, so feeding may continue.
func (r *Renderer) Finish() []Segment {
	if r.buf != "" {
		line := r.buf
		r.buf = ""
		if r.mode == modeThinking {
			r.thinkingLine(line)
		} else {
			r.textLine(line)
		}
	}
	r.flushTable()
	return r.take()
}

// take returns the collected output and starts a new collection.
func (r *Renderer) take() []Segment {
	out := r.out
	r.out = nil
	return out
}

// emit appends prose, joining it to prose emitted just before.
func (r *Renderer) emit(text string) {
	if text == "" {
		return
	}
	if n := len(r.out); n > 0 && r.out[n-1].Table == nil {
		r.out[n-1].Text += text
		return
	}
	r.out = append(r.out, Segment{Text: text})
}

func (r *Renderer) switchToText() {
	if r.mode == modeThinking {
		if r.buf != "" {
			r.thinkingLine(r.buf)
			r.buf = ""
		}
		// Prose after thinking starts after a blank line.
		r.emit("\n")
	}
	r.mode = modeText
}

func (r *Renderer) switchToThinking() {
	if r.mode == modeText {
		if r.buf != "" {
			r.textLine(r.buf)
			r.buf = ""
		}
		r.flushTable()
	}
	r.emit(Dim+"✻ thinking"+Reset+"\n")
	r.mode = modeThinking
}

// drain renders every complete line in the buffer, keeping the partial
// tail.
func (r *Renderer) drain() {
	for {
		idx := strings.IndexByte(r.buf, '\n')
		if idx < 0 {
			return
		}
		line := r.buf[:idx]
		r.buf = r.buf[idx+1:]
		if r.mode == modeThinking {
			r.thinkingLine(line)
		} else {
			r.textLine(line)
		}
	}
}

// thinkingLine renders a thinking line dim gray, without markdown.
func (r *Renderer) thinkingLine(line string) {
	r.emit(Thinking+line+Reset+"\n")
}

// textLine renders one line of prose.
func (r *Renderer) textLine(line string) {
	stripped := displaytext.TrimLeftSpace(line)
	if r.inCode {
		if strings.HasPrefix(stripped, "```") {
			// The closing fence ends any table read inside the block.
			r.flushTable()
			r.inCode = false
			r.emit(Dim+line+Reset+"\n")
			return
		}
		// Models often put markdown tables inside fences; they are drawn
		// as tables there too.
		if strings.HasPrefix(stripped, "|") {
			r.table = append(r.table, line)
			return
		}
		// Code is indented two spaces and not parsed.
		r.emit("  "+line+"\n")
		return
	}
	if strings.HasPrefix(stripped, "```") {
		r.flushTable()
		r.inCode = true
		r.emit(Dim+line+Reset+"\n")
		return
	}
	if strings.HasPrefix(stripped, "|") {
		r.table = append(r.table, line)
		return
	}
	// Any other line ends an open table first.
	r.flushTable()
	r.emit(proseLine(line))
}

// proseLine renders a header, a bullet, or a plain line of prose.
func proseLine(line string) string {
	if level, text, ok := header(line); ok {
		if level == 1 {
			return Bold + Underline + text + Reset + "\n"
		}
		return Bold + text + Reset + "\n"
	}
	if indent, rest, ok := bullet(line); ok {
		return indent + Dim + "• " + Reset + styleInline(rest) + "\n"
	}
	return styleInline(line) + "\n"
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

// header recognizes one to six '#' followed by spaces or tabs, returning
// the level and the text after the spaces.
func header(line string) (int, string, bool) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level < 1 || level > 6 || level >= len(line) || !isBlank(line[level]) {
		return 0, "", false
	}
	rest := strings.TrimLeft(line[level:], " \t")
	return level, rest, true
}

// bullet recognizes optional leading whitespace, '-' or '*', and spaces or
// tabs, returning the indent and the text after the spaces.
func bullet(line string) (string, string, bool) {
	body := displaytext.TrimLeftSpace(line)
	indent := line[:len(line)-len(body)]
	if len(body) < 2 || (body[0] != '-' && body[0] != '*') || !isBlank(body[1]) {
		return "", "", false
	}
	return indent, strings.TrimLeft(body[1:], " \t"), true
}

// flushTable ends the table being read, if any, and emits it.
func (r *Renderer) flushTable() {
	if len(r.table) == 0 {
		return
	}
	parsed := make([][]string, len(r.table))
	for i, line := range r.table {
		parsed[i] = splitRow(line)
	}
	r.table = nil

	sepIdx := -1
	for i, cells := range parsed {
		if isSeparatorRow(cells) {
			sepIdx = i
			break
		}
	}
	numCols := 0
	for _, cells := range parsed {
		numCols = max(numCols, len(cells))
	}
	aligns := make([]Align, numCols)
	t := &Table{Aligns: aligns}
	if sepIdx >= 0 {
		for j, c := range parsed[sepIdx] {
			aligns[j] = parseAlign(c)
		}
		t.HeaderRows = parsed[:sepIdx]
		t.BodyRows = parsed[sepIdx+1:]
	} else {
		t.HeaderRows = parsed[:1]
		t.BodyRows = parsed[1:]
	}
	r.out = append(r.out, Segment{Table: t})
}

// splitRow splits a markdown table row into its trimmed cell texts.
func splitRow(line string) []string {
	s := displaytext.TrimSpace(line)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	cells := strings.Split(s, "|")
	for i, c := range cells {
		cells[i] = displaytext.TrimSpace(c)
	}
	return cells
}

// isSeparatorRow reports whether every cell is a separator cell such as
// "---", ":--", "--:", or ":-:".
func isSeparatorRow(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		d := strings.TrimSuffix(strings.TrimPrefix(c, ":"), ":")
		if d == "" || strings.Trim(d, "-") != "" {
			return false
		}
	}
	return true
}

// parseAlign reads a column's alignment from its separator cell.
func parseAlign(c string) Align {
	c = displaytext.TrimSpace(c)
	left := strings.HasPrefix(c, ":")
	right := strings.HasSuffix(c, ":")
	switch {
	case left && right:
		return AlignCenter
	case right:
		return AlignRight
	default:
		return AlignLeft
	}
}
