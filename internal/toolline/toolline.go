// Package toolline formats tool activity as single SGR-styled lines with no
// trailing newline: a tool use as "▸ Name summary" and a tool result as a
// check mark or cross with the result's first line. Activity of a subagent
// is indented two spaces.
package toolline

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stricttools/miniclaude/internal/displaytext"
)

const (
	reset = "\x1b[0m"
	bold  = "\x1b[1m"
	dim   = "\x1b[2m"
	red   = "\x1b[31m"
	// glyph colors the activity marker cyan.
	glyph = "\x1b[36m"
)

// truncate turns newlines and carriage returns into spaces and cuts the
// text to limit characters followed by an ellipsis.
func truncate(text string, limit int) string {
	text = strings.NewReplacer("\n", " ", "\r", " ").Replace(text)
	rs := []rune(text)
	if len(rs) <= limit {
		return text
	}
	return string(rs[:limit]) + "…"
}

// keyValueSummary renders an unrecognized tool's input as key=value pairs,
// keys sorted, on one truncated line.
func keyValueSummary(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	keys := displaytext.SortedKeys(input)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + truncate(displaytext.String(input[k]), 40)
	}
	return truncate(strings.Join(pairs, " "), 80)
}

// describe returns the name to display for a tool and a summary of its
// input.
func describe(name string, input map[string]any) (string, string) {
	field := func(key string) string {
		v, ok := input[key]
		if !ok {
			return ""
		}
		return displaytext.String(v)
	}
	switch name {
	case "Read", "Write", "Edit":
		return name, truncate(field("file_path"), 80)
	case "Bash":
		firstLine, _, _ := strings.Cut(field("command"), "\n")
		return name, truncate(firstLine, 80)
	case "Glob", "Grep":
		summary := field("pattern")
		if path := input["path"]; displaytext.Truthy(path) {
			summary += " in " + displaytext.String(path)
		}
		return name, truncate(summary, 80)
	case "WebFetch":
		return name, truncate(field("url"), 80)
	case "WebSearch":
		return name, truncate(field("query"), 80)
	case "Agent", "Task":
		return name, truncate(field("description"), 80)
	case "TodoWrite":
		count := 0
		if todos, ok := input["todos"].([]any); ok {
			count = len(todos)
		}
		if count == 1 {
			return name, "1 item"
		}
		return name, fmt.Sprintf("%d items", count)
	}
	if strings.HasPrefix(name, "mcp__") {
		parts := strings.Split(name, "__")
		server, tool := "", ""
		if len(parts) > 1 {
			server = parts[1]
		}
		if len(parts) > 2 {
			tool = strings.Join(parts[2:], "__")
		}
		value := ""
		if len(input) > 0 {
			// The input's first key in sorted order; a decoded map keeps no
			// order of its own.
			first := displaytext.SortedKeys(input)[0]
			value = truncate(displaytext.String(input[first]), 80)
		}
		return server + ":" + tool, value
	}
	return name, keyValueSummary(input)
}

// FormatToolUse formats a tool use as "▸ Name summary". subagent is true
// for activity inside a subagent, which is indented.
func FormatToolUse(name string, input map[string]any, subagent bool) string {
	display, summary := describe(name, input)
	line := indent(subagent) + glyph + "▸" + reset + " " + bold + display + reset
	if summary != "" {
		line += " " + summary
	}
	return line
}

func indent(subagent bool) string {
	if subagent {
		return "  "
	}
	return ""
}

// contentText turns tool result content into plain text: a string as it
// is, an array as its items one per line (an object item by its "text"
// field), and anything else as display text. Empty or null content is "".
func contentText(content json.RawMessage) (string, error) {
	if len(content) == 0 {
		return "", nil
	}
	var v any
	if err := json.Unmarshal(content, &v); err != nil {
		return "", fmt.Errorf("decoding tool result content: %w", err)
	}
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case []any:
		pieces := make([]string, len(x))
		for i, item := range x {
			if obj, ok := item.(map[string]any); ok {
				text, found := obj["text"]
				if !found {
					text = ""
				}
				pieces[i] = displaytext.String(text)
			} else {
				pieces[i] = displaytext.String(item)
			}
		}
		return strings.Join(pieces, "\n"), nil
	default:
		return displaytext.String(x), nil
	}
}

// FormatToolResult formats a tool result as a dim "✓ first line" or, for an
// error, a red "✗ first line", followed by "(+N lines)" when the result has
// more lines (trailing empty lines not counted). content is the result's
// raw JSON content; an error is returned only when it is not valid JSON.
func FormatToolResult(content json.RawMessage, isError, subagent bool) (string, error) {
	text, err := contentText(content)
	if err != nil {
		return "", err
	}
	lines := strings.Split(text, "\n")
	for len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	first := truncate(lines[0], 100)
	suffix := ""
	if extra := len(lines) - 1; extra > 0 {
		suffix = fmt.Sprintf(" (+%d lines)", extra)
	}
	if isError {
		line := indent(subagent) + red + "✗ " + first + reset
		if suffix != "" {
			line += dim + suffix + reset
		}
		return line, nil
	}
	return indent(subagent) + dim + "✓ " + first + suffix + reset, nil
}
