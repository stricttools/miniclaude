package render

import (
	"sort"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"
)

// SGR escape sequences used in rendered output.
const (
	Bold      = "\x1b[1m"
	Dim       = "\x1b[2m"
	Italic    = "\x1b[3m"
	Underline = "\x1b[4m"
	Cyan      = "\x1b[36m"
	Reset     = "\x1b[0m"
	// Thinking styles thinking content: dim and bright black (gray).
	Thinking = "\x1b[2;90m"
)

// widths measures display columns. It is fixed rather than taken from the
// locale, so output does not depend on the environment: East Asian
// ambiguous characters are one column, wide and fullwidth characters two,
// and combining and control characters none.
var widths = &runewidth.Condition{EastAsianWidth: false, StrictEmojiNeutral: true}

// runeWidth is the number of columns r occupies.
func runeWidth(r rune) int {
	return widths.RuneWidth(r)
}

// sgrEnd returns the index just past an SGR escape sequence (ESC, '[',
// digits and semicolons, 'm') starting at rs[i], or -1 when none starts
// there.
func sgrEnd(rs []rune, i int) int {
	if i+1 >= len(rs) || rs[i] != 0x1b || rs[i+1] != '[' {
		return -1
	}
	for j := i + 2; j < len(rs); j++ {
		r := rs[j]
		switch {
		case r == 'm':
			return j + 1
		case (r >= '0' && r <= '9') || r == ';':
			continue
		default:
			return -1
		}
	}
	return -1
}

// StripANSI returns the visible text of an SGR-styled string.
func StripANSI(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i := 0; i < len(rs); {
		if end := sgrEnd(rs, i); end >= 0 {
			i = end
			continue
		}
		b.WriteRune(rs[i])
		i++
	}
	return b.String()
}

// VisibleWidth is the number of columns an SGR-styled string occupies.
func VisibleWidth(s string) int {
	w := 0
	for _, r := range StripANSI(s) {
		w += runeWidth(r)
	}
	return w
}

// truncateVisible cuts an SGR-styled string to maxcols columns ending in an
// ellipsis followed by a reset. Escape sequences are kept and never counted;
// a character that would take the text past maxcols-1 columns ends it.
func truncateVisible(styled string, maxcols int) string {
	if maxcols <= 0 {
		return ""
	}
	rs := []rune(styled)
	var b strings.Builder
	visible := 0
	for i := 0; i < len(rs); {
		if end := sgrEnd(rs, i); end >= 0 {
			b.WriteString(string(rs[i:end]))
			i = end
			continue
		}
		cw := runeWidth(rs[i])
		if visible+cw > maxcols-1 {
			break
		}
		b.WriteRune(rs[i])
		visible += cw
		i++
	}
	b.WriteString("…")
	b.WriteString(Reset)
	return b.String()
}

// indexRune returns the index of the first r in rs at or after from, or -1.
func indexRune(rs []rune, r rune, from int) int {
	for i := from; i < len(rs); i++ {
		if rs[i] == r {
			return i
		}
	}
	return -1
}

// hasDoubleStar reports whether rs holds "**" at i.
func hasDoubleStar(rs []rune, i int) bool {
	return i+1 < len(rs) && rs[i] == '*' && rs[i+1] == '*'
}

// indexDoubleStar returns the index of the first "**" in rs at or after
// from, or -1.
func indexDoubleStar(rs []rune, from int) int {
	for i := from; i+1 < len(rs); i++ {
		if hasDoubleStar(rs, i) {
			return i
		}
	}
	return -1
}

// styleInline applies inline markdown: `code` in cyan (its content not
// parsed further), [text](url) as underlined text and a dim url, **bold**,
// and *italic* or _italic_. A marker without its closing partner stays
// literal.
func styleInline(s string) string {
	rs := []rune(s)
	n := len(rs)
	var b strings.Builder
	for i := 0; i < n; {
		c := rs[i]
		switch {
		case c == '`':
			if end := indexRune(rs, '`', i+1); end != -1 {
				b.WriteString(Cyan + string(rs[i+1:end]) + Reset)
				i = end + 1
				continue
			}
		case c == '[':
			closing := indexRune(rs, ']', i+1)
			if closing != -1 && closing+1 < n && rs[closing+1] == '(' {
				if urlEnd := indexRune(rs, ')', closing+2); urlEnd != -1 {
					text := string(rs[i+1 : closing])
					url := string(rs[closing+2 : urlEnd])
					b.WriteString(Underline + text + Reset + " (" + Dim + url + Reset + ")")
					i = urlEnd + 1
					continue
				}
			}
		case hasDoubleStar(rs, i):
			if end := indexDoubleStar(rs, i+2); end != -1 {
				b.WriteString(Bold + string(rs[i+2:end]) + Reset)
				i = end + 2
				continue
			}
		case c == '*' || c == '_':
			if end := indexRune(rs, c, i+1); end != -1 && end > i+1 {
				b.WriteString(Italic + string(rs[i+1:end]) + Reset)
				i = end + 1
				continue
			}
		}
		b.WriteRune(c)
		i++
	}
	return b.String()
}

// sgrParams parses the parameters of an SGR escape sequence: "\x1b[2;90m"
// gives [2 90] and "\x1b[m" gives [0]. Empty parameters are skipped; a
// parameter too large to parse is kept as -1, which no rule matches.
func sgrParams(escape string) []int {
	inner := strings.TrimSuffix(strings.TrimPrefix(escape, "\x1b["), "m")
	if inner == "" {
		return []int{0}
	}
	var params []int
	for _, p := range strings.Split(inner, ";") {
		if p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			v = -1
		}
		params = append(params, v)
	}
	if len(params) == 0 {
		return []int{0}
	}
	return params
}

// sgrState is the set of active SGR codes.
type sgrState map[int]bool

func isForeground(c int) bool { return (c >= 30 && c <= 37) || (c >= 90 && c <= 97) }

func isBackground(c int) bool { return (c >= 40 && c <= 47) || (c >= 100 && c <= 107) }

// update applies an SGR escape sequence to the state: a reset clears it,
// bold, dim, italic, and underline are added and removed by their codes,
// and a color replaces the previous color of its layer.
func (s sgrState) update(escape string) {
	for _, p := range sgrParams(escape) {
		switch {
		case p == 0:
			clear(s)
		case p >= 1 && p <= 4:
			s[p] = true
		case p == 22:
			delete(s, 1)
			delete(s, 2)
		case p == 23:
			delete(s, 3)
		case p == 24:
			delete(s, 4)
		case isForeground(p):
			s.deleteWhere(isForeground)
			s[p] = true
		case p == 39:
			s.deleteWhere(isForeground)
		case isBackground(p):
			s.deleteWhere(isBackground)
			s[p] = true
		case p == 49:
			s.deleteWhere(isBackground)
		}
	}
}

func (s sgrState) deleteWhere(match func(int) bool) {
	for c := range s {
		if match(c) {
			delete(s, c)
		}
	}
}

func (s sgrState) clone() sgrState {
	c := make(sgrState, len(s))
	for k := range s {
		c[k] = true
	}
	return c
}

// escape returns one SGR escape sequence restoring the state, or "" when
// the state is empty.
func (s sgrState) escape() string {
	if len(s) == 0 {
		return ""
	}
	codes := make([]int, 0, len(s))
	for c := range s {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	parts := make([]string, len(codes))
	for i, c := range codes {
		parts[i] = strconv.Itoa(c)
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}
