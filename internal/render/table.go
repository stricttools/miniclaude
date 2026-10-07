package render

import (
	"sort"
	"strings"
)

// Align is a table column's alignment.
type Align int

const (
	AlignLeft Align = iota
	AlignCenter
	AlignRight
)

// Table is a markdown table's raw cell text, styled only when rendered, so
// it can be rendered again at any width.
type Table struct {
	HeaderRows [][]string
	BodyRows   [][]string
	// Aligns holds one alignment per column; missing columns align left.
	Aligns []Align
}

// cell is a styled cell and its width in columns.
type cell struct {
	styled string
	width  int
}

// Render draws the table with box-drawing borders to fit width columns,
// one string per terminal row with no trailing newline. Column widths are
// fitted in three tiers (natural widths, then floors of the header and
// longest-word widths, then shares of the floors), and cells wrap at word
// boundaries, keeping their SGR state across the wrapped rows.
func (t *Table) Render(width int) []string {
	numCols := 1
	for _, rows := range [][][]string{t.HeaderRows, t.BodyRows} {
		for _, r := range rows {
			numCols = max(numCols, len(r))
		}
	}
	aligns := make([]Align, numCols)
	copy(aligns, t.Aligns)

	build := func(rows [][]string, isHeader bool) [][]cell {
		grid := make([][]cell, 0, len(rows))
		for _, r := range rows {
			cells := make([]cell, numCols)
			for j := range numCols {
				text := ""
				if j < len(r) {
					text = r[j]
				}
				styled := styleInline(text)
				if isHeader {
					styled = Bold + styled + Reset
				}
				cells[j] = cell{styled: styled, width: VisibleWidth(styled)}
			}
			grid = append(grid, cells)
		}
		return grid
	}
	headerGrid := build(t.HeaderRows, true)
	bodyGrid := build(t.BodyRows, false)

	naturalW := make([]int, numCols)
	headerW := make([]int, numCols)
	wordW := make([]int, numCols)
	for _, cells := range headerGrid {
		for j, c := range cells {
			naturalW[j] = max(naturalW[j], c.width)
			headerW[j] = max(headerW[j], c.width)
			wordW[j] = max(wordW[j], longestWordWidth(c.styled))
		}
	}
	for _, cells := range bodyGrid {
		for j, c := range cells {
			naturalW[j] = max(naturalW[j], c.width)
			wordW[j] = max(wordW[j], longestWordWidth(c.styled))
		}
	}
	floors := make([]int, numCols)
	for j := range numCols {
		floors[j] = max(headerW[j], wordW[j], 1)
	}
	colW := fitWidths(naturalW, width, floors)

	lines := []string{border(colW, "┌", "┬", "┐", "─")}
	for _, cells := range headerGrid {
		lines = append(lines, wrappedRow(cells, colW, aligns)...)
	}
	if len(headerGrid) > 0 {
		lines = append(lines, border(colW, "╞", "╪", "╡", "═"))
	}
	for i, cells := range bodyGrid {
		if i > 0 {
			// A light rule between logical body rows, never between the
			// wrapped rows of one logical row.
			lines = append(lines, border(colW, "├", "┼", "┤", "─"))
		}
		lines = append(lines, wrappedRow(cells, colW, aligns)...)
	}
	lines = append(lines, border(colW, "└", "┴", "┘", "─"))
	return lines
}

// border draws a horizontal rule across the columns.
func border(colW []int, left, join, right, fill string) string {
	parts := make([]string, len(colW))
	for i, w := range colW {
		parts[i] = strings.Repeat(fill, w+2)
	}
	return left + strings.Join(parts, join) + right
}

// wrappedRow draws one logical row: each cell is wrapped to its column,
// and the row is as many terminal rows as its tallest cell.
func wrappedRow(cells []cell, colW []int, aligns []Align) []string {
	numCols := len(colW)
	wrapped := make([][]string, 0, numCols)
	for j, c := range cells {
		wrapped = append(wrapped, wrapCell(c.styled, colW[j]))
	}
	for len(wrapped) < numCols {
		wrapped = append(wrapped, []string{""})
	}
	height := 1
	if len(wrapped) > 0 {
		height = 0
		for _, w := range wrapped {
			height = max(height, len(w))
		}
	}
	out := make([]string, 0, height)
	for i := range height {
		parts := make([]string, numCols)
		for j := range numCols {
			sub, visW := "", 0
			if i < len(wrapped[j]) {
				sub = wrapped[j][i]
				visW = VisibleWidth(sub)
			}
			parts[j] = pad(sub, visW, colW[j], aligns[j])
		}
		out = append(out, "│ "+strings.Join(parts, " │ ")+" │")
	}
	return out
}

// pad pads or truncates a styled cell to target columns.
func pad(styled string, visible, target int, align Align) string {
	if visible > target {
		styled = truncateVisible(styled, target)
		// A wide character that did not fit before the ellipsis leaves
		// the result narrower than the target, so measure again.
		visible = VisibleWidth(styled)
	}
	gap := target - visible
	switch align {
	case AlignRight:
		return strings.Repeat(" ", gap) + styled
	case AlignCenter:
		left := gap / 2
		return strings.Repeat(" ", left) + styled + strings.Repeat(" ", gap-left)
	default:
		return styled + strings.Repeat(" ", gap)
	}
}

// longestWordWidth is the width of the longest segment of styled text that
// cannot be broken. Text breaks at spaces and after hyphens.
func longestWordWidth(styled string) int {
	maxW, curW := 0, 0
	for _, c := range StripANSI(styled) {
		switch c {
		case ' ':
			maxW = max(maxW, curW)
			curW = 0
		case '-':
			curW += runeWidth(c)
			maxW = max(maxW, curW)
			curW = 0
		default:
			curW += runeWidth(c)
		}
	}
	return max(maxW, curW)
}

// fitWidths allocates column widths for a table of total width columns.
// Natural widths are used when they fit; otherwise each column gets its
// floor and the rest is shared in proportion to how far each column's
// natural width exceeds its floor; when even the floors do not fit, the
// space is shared in proportion to the floors.
func fitWidths(naturalW []int, width int, floors []int) []int {
	numCols := len(naturalW)
	available := width - (3*numCols + 1)
	result := make([]int, numCols)

	if available <= 0 {
		for j, f := range floors {
			result[j] = max(f, 1)
		}
		return result
	}

	if sum(naturalW) <= available {
		copy(result, naturalW)
		return result
	}

	floorTotal := sum(floors)
	if floorTotal <= available {
		remaining := available - floorTotal
		excess := make([]int, numCols)
		for j := range numCols {
			excess[j] = max(0, naturalW[j]-floors[j])
		}
		totalExcess := sum(excess)
		copy(result, floors)
		if totalExcess > 0 {
			for j := range numCols {
				result[j] += remaining * excess[j] / totalExcess
			}
			leftover := remaining - (sum(result) - floorTotal)
			order := descendingOrder(excess)
			for k := 0; k < min(leftover, len(order)); k++ {
				result[order[k]]++
			}
		}
		return result
	}

	if floorTotal > 0 {
		for j := range numCols {
			result[j] = max(1, available*floors[j]/floorTotal)
		}
		leftover := available - sum(result)
		order := descendingOrder(floors)
		for k := 0; k < max(0, min(leftover, len(order))); k++ {
			result[order[k]]++
		}
		return result
	}

	for j := range result {
		result[j] = 1
	}
	return result
}

// descendingOrder returns the indices of values ordered by value, largest
// first, equal values keeping their index order.
func descendingOrder(values []int) []int {
	order := make([]int, len(values))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return values[order[a]] > values[order[b]] })
	return order
}

func sum(values []int) int {
	total := 0
	for _, v := range values {
		total += v
	}
	return total
}

// wrapToken is a piece of cell text: an SGR escape sequence or one
// character with its width.
type wrapToken struct {
	escape bool
	text   string
	width  int
}

func joinTokens(tokens []wrapToken) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteString(t.text)
	}
	return b.String()
}

// emitLine joins tokens into one wrapped row, ending it with a reset when
// state is active.
func emitLine(tokens []wrapToken, state sgrState) string {
	s := joinTokens(tokens)
	if len(state) > 0 {
		s += Reset
	}
	return s
}

// statePrefix starts a wrapped row by restoring state.
func statePrefix(state sgrState) []wrapToken {
	if seq := state.escape(); seq != "" {
		return []wrapToken{{escape: true, text: seq}}
	}
	return nil
}

func visibleTokenWidth(tokens []wrapToken) int {
	w := 0
	for _, t := range tokens {
		if !t.escape {
			w += t.width
		}
	}
	return w
}

// wrapCell wraps styled text into rows of at most width columns. It breaks
// at spaces and after hyphens when it can, and inside a word only when the
// word is wider than the column. Each row ends with a reset when styling is
// active and the next starts by restoring it. A character wider than the
// column is placed on a row of its own rather than dropped.
func wrapCell(text string, width int) []string {
	if width <= 0 || text == "" {
		return []string{""}
	}

	rs := []rune(text)
	var tokens []wrapToken
	for i := 0; i < len(rs); {
		if end := sgrEnd(rs, i); end >= 0 {
			tokens = append(tokens, wrapToken{escape: true, text: string(rs[i:end])})
			i = end
			continue
		}
		tokens = append(tokens, wrapToken{text: string(rs[i]), width: runeWidth(rs[i])})
		i++
	}

	var lines []string
	var cur []wrapToken
	curW := 0
	brk := -1 // index in cur just after the last break character
	brkState := sgrState{}
	state := sgrState{}

	for _, tok := range tokens {
		if tok.escape {
			state.update(tok.text)
			cur = append(cur, tok)
			continue
		}
		ch, cw := tok.text, tok.width

		if curW+cw > width {
			if ch == " " {
				// A space at the overflow point ends the row and is
				// consumed.
				lines = append(lines, emitLine(cur, state))
				cur = statePrefix(state)
				curW = 0
				brk = -1
				continue
			}
			if brk >= 0 {
				before := cur[:brk]
				after := append([]wrapToken(nil), cur[brk:]...)
				lines = append(lines, emitLine(before, brkState))
				cur = append(statePrefix(brkState), after...)
				curW = visibleTokenWidth(after)
				brk = -1
			} else if curW > 0 {
				// A row holding only escape sequences has no width and
				// is never emitted on its own.
				lines = append(lines, emitLine(cur, state))
				cur = statePrefix(state)
				curW = 0
				brk = -1
			}
		}

		// The text left over after a word break may still overflow with
		// this character.
		if curW+cw > width && curW > 0 {
			lines = append(lines, emitLine(cur, state))
			cur = statePrefix(state)
			curW = 0
			brk = -1
		}

		cur = append(cur, tok)
		curW += cw

		if ch == " " || ch == "-" {
			brk = len(cur)
			brkState = state.clone()
		}
	}

	if len(cur) > 0 || len(lines) == 0 {
		lines = append(lines, emitLine(cur, state))
	}
	return lines
}
