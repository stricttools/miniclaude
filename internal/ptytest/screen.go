package ptytest

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// continuation marks the cells a wide character covers after its first.
const continuation = "\x00"

// Screen is a model of the terminal the REPL draws on, enough for the
// sequences it writes: cursor positioning (CSI row;col H), erasing a line
// (CSI 2K) and the screen (CSI 2J), the alternate screen, and text with
// autowrap off. Colors and private modes are read and ignored.
type Screen struct {
	Rows, Cols int
	cells      [][]string
	row, col   int
	// Alternate reports whether the alternate screen is in use.
	Alternate bool
}

// NewScreen returns a blank screen of rows by cols.
func NewScreen(rows, cols int) *Screen {
	s := &Screen{Rows: rows, Cols: cols}
	s.clear()
	return s
}

func (s *Screen) clear() {
	s.cells = make([][]string, s.Rows)
	for i := range s.cells {
		s.cells[i] = make([]string, s.Cols)
	}
}

// Feed applies everything output holds, in order.
func (s *Screen) Feed(output string) {
	for i := 0; i < len(output); {
		if output[i] == 0x1b && i+1 < len(output) {
			i = s.escape(output, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(output[i:])
		switch r {
		case '\r':
			s.col = 0
		case '\n':
			if s.row < s.Rows-1 {
				s.row++
			}
		default:
			s.put(string(r), runewidth.RuneWidth(r))
		}
		i += size
	}
}

func (s *Screen) put(text string, width int) {
	if width == 0 || s.row >= s.Rows {
		return
	}
	if s.col+width > s.Cols {
		return // autowrap is off: the character is not drawn
	}
	s.cells[s.row][s.col] = text
	for k := 1; k < width; k++ {
		s.cells[s.row][s.col+k] = continuation
	}
	s.col += width
}

// escape applies the escape sequence at output[i] and returns the index
// after it.
func (s *Screen) escape(output string, i int) int {
	switch output[i+1] {
	case '[':
		j := i + 2
		for j < len(output) && (output[j] < 0x40 || output[j] > 0x7e) {
			j++
		}
		if j >= len(output) {
			return len(output)
		}
		s.csi(output[i+2:j], output[j])
		return j + 1
	case ']':
		// OSC: up to BEL or ST.
		j := i + 2
		for j < len(output) && output[j] != 0x07 && !(output[j] == 0x1b && j+1 < len(output) && output[j+1] == '\\') {
			j++
		}
		if j < len(output) && output[j] == 0x1b {
			j++
		}
		return j + 1
	}
	return i + 2
}

func (s *Screen) csi(params string, final byte) {
	if strings.HasPrefix(params, "?") {
		if params == "?1049" && (final == 'h' || final == 'l') {
			s.Alternate = final == 'h'
			s.clear()
		}
		return
	}
	num := func(k, def int) int {
		parts := strings.Split(params, ";")
		if k >= len(parts) || parts[k] == "" {
			return def
		}
		n, err := strconv.Atoi(parts[k])
		if err != nil {
			return def
		}
		return n
	}
	switch final {
	case 'H':
		s.row, s.col = min(num(0, 1), s.Rows)-1, min(num(1, 1), s.Cols)-1
	case 'K':
		if num(0, 0) == 2 && s.row < s.Rows {
			s.cells[s.row] = make([]string, s.Cols)
		}
	case 'J':
		if num(0, 0) == 2 {
			s.clear()
		}
	}
}

// Lines returns every row's text, trailing blanks trimmed.
func (s *Screen) Lines() []string {
	out := make([]string, s.Rows)
	for i, row := range s.cells {
		var b strings.Builder
		for _, c := range row {
			if c == continuation {
				continue
			}
			if c == "" {
				b.WriteString(" ")
				continue
			}
			b.WriteString(c)
		}
		out[i] = strings.TrimRight(b.String(), " ")
	}
	return out
}

// Text returns the rows joined by newlines.
func (s *Screen) Text() string {
	return strings.Join(s.Lines(), "\n")
}
