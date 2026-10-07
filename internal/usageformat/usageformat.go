// Package usageformat formats what miniclaude reports about usage: the
// line closing each turn, the session cost line, the context-window
// listing, and the context-window percentage. Each line is dim (red for a
// failed turn) and ends in a newline.
package usageformat

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

const reset = "\x1b[0m"

func dim(text string) string { return "\x1b[2m" + text + reset }

func red(text string) string { return "\x1b[31m" + text + reset }

// ModelUsage is one model's token usage in a turn's result.
type ModelUsage struct {
	InputTokens              int64
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
	// ContextWindow is the model's context window in tokens; zero when
	// the result does not report it.
	ContextWindow int64
}

// ContextPercent is the share of the context window in use, as a rounded
// percentage: input, cache-read, and cache-creation tokens over the
// context window. It reads the entry of model when that entry reports a
// context window, and otherwise the first entry in model-name order that
// does. It reports false when no entry does.
func ContextPercent(usage map[string]ModelUsage, model string) (int, bool) {
	entry, ok := usage[model]
	if model == "" || !ok || entry.ContextWindow == 0 {
		names := make([]string, 0, len(usage))
		for name := range usage {
			names = append(names, name)
		}
		sort.Strings(names)
		ok = false
		for _, name := range names {
			if usage[name].ContextWindow != 0 {
				entry, ok = usage[name], true
				break
			}
		}
		if !ok {
			return 0, false
		}
	}
	used := entry.InputTokens + entry.CacheReadInputTokens + entry.CacheCreationInputTokens
	return percent(used, entry.ContextWindow), true
}

// percent is 100*part/whole rounded to the nearest integer, halves to even.
func percent(part, whole int64) int {
	return int(math.RoundToEven(100*float64(part)/float64(whole)))
}

// Result is the part of a turn's result the closing line reports.
type Result struct {
	TotalCostUSD float64
	DurationMS   float64
	NumTurns     int
	IsError      bool
	ModelUsage   map[string]ModelUsage
}

// ResultLine is the line closing a turn: "── $cost · 2.3s · N turn(s)",
// with " · ctx N%" when the context percentage is known, and marked
// "error · " and red when the turn failed. model names the session's model
// for ContextPercent; empty when unknown.
func ResultLine(r Result, model string) string {
	parts := []string{
		fmt.Sprintf("$%.4f", r.TotalCostUSD),
		fmt.Sprintf("%.1fs", r.DurationMS/1000),
		fmt.Sprintf("%d turn(s)", r.NumTurns),
	}
	line := "── "
	if r.IsError {
		line += "error · "
	}
	line += strings.Join(parts, " · ")
	if pct, ok := ContextPercent(r.ModelUsage, model); ok {
		line += fmt.Sprintf(" · ctx %d%%", pct)
	}
	if r.IsError {
		return red(line) + "\n"
	}
	return dim(line) + "\n"
}

// CostLine is the session totals line: "── $cost · N tokens · N turn(s)".
func CostLine(totalCostUSD float64, totalTokens int64, turns int) string {
	return dim(fmt.Sprintf("── $%.4f · %d tokens · %d turn(s)", totalCostUSD, totalTokens, turns)) + "\n"
}

// ContextCategory is one category of the context-window breakdown.
type ContextCategory struct {
	Name   string
	Tokens int64
}

// ContextUsage is the context-window breakdown the session reports.
type ContextUsage struct {
	Categories  []ContextCategory
	TotalTokens int64
	MaxTokens   int64
}

// ContextListing lists the context window: one line per category, names
// padded to a common width, then "total used/max (N%)", the percentage 0
// when the maximum is 0.
func ContextListing(u ContextUsage) string {
	width := 0
	for _, c := range u.Categories {
		width = max(width, utf8.RuneCountInString(c.Name))
	}
	var b strings.Builder
	for _, c := range u.Categories {
		name := c.Name + strings.Repeat(" ", width-utf8.RuneCountInString(c.Name))
		b.WriteString(dim(fmt.Sprintf("  %s  %d", name, c.Tokens))+"\n")
	}
	pct := 0
	if u.MaxTokens != 0 {
		pct = percent(u.TotalTokens, u.MaxTokens)
	}
	b.WriteString(dim(fmt.Sprintf("  total %d/%d (%d%%)", u.TotalTokens, u.MaxTokens, pct))+"\n")
	return b.String()
}
