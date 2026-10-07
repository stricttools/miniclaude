package render

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// fixture is one case of the Python's renderer battery and what the Python
// rendered for it (scripts/python-expectations render-fixtures).
type fixture struct {
	Name     string      `json:"name"`
	Width    int         `json:"width"`
	Segments [][2]string `json:"segments"`
	Output   string      `json:"output"`
}

func loadFixtures(t *testing.T) []fixture {
	t.Helper()
	data, err := os.ReadFile("testdata/python-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []fixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

// run feeds segments in chunks of the cycling sizes (nil feeds each whole)
// and returns the output with every table rendered at width.
func run(segments [][2]string, width int, sizes []int) string {
	r := New()
	var out []Segment
	for _, seg := range segments {
		feed := r.FeedText
		if seg[0] == "thinking" {
			feed = r.FeedThinking
		}
		text := []rune(seg[1])
		if sizes == nil {
			out = append(out, feed(seg[1])...)
			continue
		}
		for i, k := 0, 0; i < len(text); k++ {
			n := min(sizes[k%len(sizes)], len(text)-i)
			out = append(out, feed(string(text[i:i+n]))...)
			i += n
		}
	}
	out = append(out, r.Finish()...)
	var b strings.Builder
	for _, s := range out {
		if s.Table != nil {
			for _, row := range s.Table.Render(width) {
				b.WriteString(row + "\n")
			}
			continue
		}
		b.WriteString(s.Text)
	}
	return b.String()
}

func TestRendererMatchesThePython(t *testing.T) {
	for _, f := range loadFixtures(t) {
		if got := run(f.Segments, f.Width, nil); got != f.Output {
			t.Errorf("%s at width %d:\n got: %q\nwant: %q", f.Name, f.Width, got, f.Output)
		}
	}
}

func TestOutputDoesNotDependOnChunking(t *testing.T) {
	for _, f := range loadFixtures(t) {
		whole := run(f.Segments, f.Width, nil)
		for _, sizes := range [][]int{{1}, {2}, {3}, {1, 3, 2, 5}, {7, 1}} {
			if got := run(f.Segments, f.Width, sizes); got != whole {
				t.Errorf("%s chunked by %v:\n got: %q\nwant: %q", f.Name, sizes, got, whole)
			}
		}
	}
}

func TestTableRowsFitTheWidth(t *testing.T) {
	segs := New().FeedText("| Name | Description |\n| --- | --- |\n| Alice | A very long description that wraps around and around |\n")
	segs = append(segs, New().Finish()...)
	var table *Table
	for _, s := range segs {
		if s.Table != nil {
			table = s.Table
		}
	}
	if table == nil {
		r := New()
		for _, s := range append(r.FeedText("| A | B |\n| - | - |\n| 1 | 2 |\n"), r.Finish()...) {
			if s.Table != nil {
				table = s.Table
			}
		}
	}
	for _, width := range []int{80, 40, 20, 12} {
		for _, row := range table.Render(width) {
			if w := VisibleWidth(row); w > width {
				t.Errorf("width %d: row %q is %d wide", width, StripANSI(row), w)
			}
		}
	}
}
