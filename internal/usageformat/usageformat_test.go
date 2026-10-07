package usageformat

import "testing"

// The expected values are what the Python's _ctx_pct, _fmt_duration, and
// result, context, and cost lines produce for the same inputs.
func TestContextPercent(t *testing.T) {
	cases := []struct {
		usage map[string]ModelUsage
		model string
		want  int
		ok    bool
	}{
		{map[string]ModelUsage{"a": {ContextWindow: 200000, InputTokens: 1000}}, "a", 0, true},
		{map[string]ModelUsage{"a": {ContextWindow: 200, InputTokens: 1}}, "a", 0, true},
		{map[string]ModelUsage{"a": {ContextWindow: 200, InputTokens: 3}}, "a", 2, true},
		{map[string]ModelUsage{"m": {InputTokens: 5}, "z": {ContextWindow: 100, InputTokens: 25, CacheReadInputTokens: 10, CacheCreationInputTokens: 5}}, "m", 40, true},
		{nil, "m", 0, false},
		{map[string]ModelUsage{"m": {InputTokens: 5}}, "m", 0, false},
	}
	for i, c := range cases {
		got, ok := ContextPercent(c.usage, c.model)
		if got != c.want || ok != c.ok {
			t.Errorf("case %d: %d %v", i, got, ok)
		}
	}
}

func TestLines(t *testing.T) {
	r := Result{TotalCostUSD: 0.00125, DurationMS: 2349.9, NumTurns: 2, ModelUsage: map[string]ModelUsage{"m": {ContextWindow: 200, InputTokens: 3}}}
	if got := ResultLine(r, "m"); got != "\x1b[2m── $0.0013 · 2.3s · 2 turn(s) · ctx 2%\x1b[0m\n" {
		t.Errorf("result line %q", got)
	}
	r.IsError, r.ModelUsage, r.DurationMS = true, nil, 1050
	if got := ResultLine(r, ""); got != "\x1b[31m── error · $0.0013 · 1.1s · 2 turn(s)\x1b[0m\n" {
		t.Errorf("error line %q", got)
	}
	if got := CostLine(1.5, 1234, 3); got != "\x1b[2m── $1.5000 · 1234 tokens · 3 turn(s)\x1b[0m\n" {
		t.Errorf("cost line %q", got)
	}
	got := ContextListing(ContextUsage{Categories: []ContextCategory{{"system", 10}, {"messages", 2000}}, TotalTokens: 2010, MaxTokens: 200000})
	want := "\x1b[2m  system    10\x1b[0m\n\x1b[2m  messages  2000\x1b[0m\n\x1b[2m  total 2010/200000 (1%)\x1b[0m\n"
	if got != want {
		t.Errorf("context listing\n got: %q\nwant: %q", got, want)
	}
	if got := ContextListing(ContextUsage{}); got != "\x1b[2m  total 0/0 (0%)\x1b[0m\n" {
		t.Errorf("empty listing %q", got)
	}
}
