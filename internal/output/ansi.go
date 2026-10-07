package output

import (
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// reset is the SGR sequence that clears every character attribute.
const reset = "\x1b[0m"

// widths measures cells with a fixed condition rather than one taken from
// the locale, the condition the renderer measures with: East Asian
// ambiguous characters take one cell, wide and fullwidth characters two.
var widths = &runewidth.Condition{EastAsianWidth: false, StrictEmojiNeutral: true}

// displayRune returns how r is drawn and its width in cells. Control
// characters are drawn in caret notation (a tab is "^I"), as prompt_toolkit
// draws them; C1 controls are drawn as "?". Every other rune is drawn as
// itself, its width from widths.
func displayRune(r rune) (string, int) {
	switch {
	case r < 0x20:
		return "^" + string(r+0x40), 2
	case r == 0x7f:
		return "^?", 2
	case r >= 0x80 && r < 0xa0:
		return "?", 1
	}
	return string(r), widths.RuneWidth(r)
}

// DisplayRune returns how r is drawn in a terminal cell run and its width
// in cells, by the same rules the output rows use, so the input editor
// measures its text the way the output measures its rows.
func DisplayRune(r rune) (string, int) {
	return displayRune(r)
}

// nextToken returns the token at the start of s: an escape sequence (esc
// set) or one rune. A lone or unfinished ESC is returned as the rune ESC,
// so it is drawn instead of corrupting the terminal.
func nextToken(s string) (tok string, esc bool, r rune) {
	if s[0] == 0x1b && len(s) > 1 {
		switch s[1] {
		case '[':
			for j := 2; j < len(s); j++ {
				c := s[j]
				if c >= 0x40 && c <= 0x7e {
					return s[:j+1], true, 0
				}
				if c < 0x20 || c > 0x3f {
					break
				}
			}
		case ']':
			// An OSC sequence ends at BEL or at ST (ESC \), whichever is first.
			bel := strings.IndexByte(s, 0x07)
			st := strings.Index(s, "\x1b\\")
			switch {
			case st >= 0 && (bel < 0 || st < bel):
				return s[:st+2], true, 0
			case bel >= 0:
				return s[:bel+1], true, 0
			}
		default:
			if s[1] >= 0x20 && s[1] <= 0x7e {
				return s[:2], true, 0
			}
		}
	}
	r, n := utf8.DecodeRuneInString(s)
	return s[:n], false, r
}

// sgrState is the character attributes in force at a point of a line: the
// SGR sequences applied since the last full reset, in order.
type sgrState struct {
	seqs []string
}

// isSGR reports whether the escape sequence seq is an SGR sequence.
func isSGR(seq string) bool {
	return len(seq) >= 3 && seq[1] == '[' && seq[len(seq)-1] == 'm'
}

// apply advances the state past the SGR sequence seq. A sequence whose
// first parameter is empty or 0 resets every attribute before setting the
// rest.
func (st *sgrState) apply(seq string) {
	params := seq[2 : len(seq)-1]
	first, _, _ := strings.Cut(params, ";")
	if first == "" || first == "0" {
		st.seqs = nil
		if params == "" || params == "0" {
			return
		}
	}
	st.seqs = append(st.seqs, seq)
}

// active reports whether any attribute is set.
func (st *sgrState) active() bool {
	return len(st.seqs) > 0
}

// prefix returns the sequences that re-establish the state on a fresh row.
func (st *sgrState) prefix() string {
	return strings.Join(st.seqs, "")
}

// clone returns a copy that shares nothing with st.
func (st *sgrState) clone() sgrState {
	return sgrState{seqs: append([]string(nil), st.seqs...)}
}

// advance moves the state past every SGR sequence in line.
func (st *sgrState) advance(line string) {
	for len(line) > 0 {
		tok, esc, _ := nextToken(line)
		if esc && isSGR(tok) {
			st.apply(tok)
		}
		line = line[len(tok):]
	}
}

// wrapLine hard-wraps line, which holds no newline, into rows of at most
// width cells. It starts in state st and leaves st at the state after the
// line. Every row starts by re-establishing the attributes in force where it
// begins and ends with a reset when any are set, so each row draws
// correctly on its own. A rune wider than width gets a row of its own.
func wrapLine(line string, width int, st *sgrState) []string {
	if width < 1 {
		width = 1
	}
	var rows []string
	var row strings.Builder
	row.WriteString(st.prefix())
	col := 0
	for len(line) > 0 {
		tok, esc, r := nextToken(line)
		line = line[len(tok):]
		if esc {
			row.WriteString(tok)
			if isSGR(tok) {
				st.apply(tok)
			}
			continue
		}
		disp, w := displayRune(r)
		if col > 0 && col+w > width {
			if st.active() {
				row.WriteString(reset)
			}
			rows = append(rows, row.String())
			row.Reset()
			row.WriteString(st.prefix())
			col = 0
		}
		row.WriteString(disp)
		col += w
	}
	if st.active() {
		row.WriteString(reset)
	}
	return append(rows, row.String())
}

// Wrap hard-wraps one line, which holds no newline, into rows of at most
// width cells, carrying its SGR attributes onto every row.
func Wrap(line string, width int) []string {
	var st sgrState
	return wrapLine(line, width, &st)
}

// Truncate cuts line, which holds no newline, to at most width cells. Its
// escape sequences before the cut are kept, and a reset ends the result
// when any escape sequence was kept.
func Truncate(line string, width int) string {
	var out strings.Builder
	col := 0
	escaped := false
	for len(line) > 0 {
		tok, esc, r := nextToken(line)
		line = line[len(tok):]
		if esc {
			out.WriteString(tok)
			escaped = true
			continue
		}
		disp, w := displayRune(r)
		if col+w > width {
			break
		}
		out.WriteString(disp)
		col += w
	}
	if escaped {
		out.WriteString(reset)
	}
	return out.String()
}

// Width returns the number of cells line takes, escape sequences excluded.
func Width(line string) int {
	col := 0
	for len(line) > 0 {
		tok, esc, r := nextToken(line)
		line = line[len(tok):]
		if !esc {
			_, w := displayRune(r)
			col += w
		}
	}
	return col
}
