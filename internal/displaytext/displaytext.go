// Package displaytext turns decoded JSON values and multi-line strings into
// the text miniclaude shows. Values are the ones encoding/json produces when
// decoding into any: nil, bool, float64, string, []any, and map[string]any.
// A value is shown the way the Python miniclaude showed it with str():
// strings verbatim, true and false as True and False, null as None, and
// arrays and objects in Python's literal notation with quoted strings.
// Object keys are shown sorted, since a decoded Go map keeps no order.
package displaytext

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// String renders v as display text: a string verbatim, anything else as
// its literal notation.
func String(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return literal(v)
}

// literal renders v in Python's literal notation, strings quoted.
func literal(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return quote(x)
	case float64:
		return number(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case json.Number:
		return x.String()
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = literal(item)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := SortedKeys(x)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = quote(k) + ": " + literal(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	default:
		return fmt.Sprint(x)
	}
}

// number renders a float. Integral values below 1e16 are shown as integers,
// because JSON integers decode to float64 and were integers in the Python;
// other values use the shortest digits, in exponent form below 1e-4 or from
// 1e16 on.
func number(f float64) string {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	if f == 0 {
		return "0"
	}
	abs := math.Abs(f)
	if f == math.Trunc(f) && abs < 1e16 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	if abs < 1e-4 || abs >= 1e16 {
		return strconv.FormatFloat(f, 'e', -1, 64)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// quote renders s as a Python string literal: single quotes unless s holds
// a single quote and no double quote, with backslash escapes for the quote,
// backslashes, tabs, newlines, carriage returns, and unprintable characters.
func quote(s string) string {
	q := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		q = '"'
	}
	var b strings.Builder
	b.WriteRune(q)
	for _, r := range s {
		switch {
		case r == q || r == '\\':
			b.WriteRune('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteRune(q)
	return b.String()
}

// Truthy reports whether v counts as true: a non-empty string, array, or
// object, a non-zero number, or true.
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	default:
		return true
	}
}

// Compact encodes v as compact JSON without escaping HTML characters, the
// form the Python wrote with json.dumps(v, separators=(",", ":"),
// ensure_ascii=False).
func Compact(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("encoding a value as JSON: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// SortedKeys returns the keys of m in ascending order.
func SortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// IsSpace reports whether r is whitespace in the sense of Python's
// str.isspace, which adds the file, group, record, and unit separators
// (U+001C to U+001F) to Unicode's white space.
func IsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// TrimSpace removes leading and trailing whitespace as IsSpace defines it.
func TrimSpace(s string) string {
	return strings.TrimFunc(s, IsSpace)
}

// TrimLeftSpace removes leading whitespace as IsSpace defines it.
func TrimLeftSpace(s string) string {
	return strings.TrimLeftFunc(s, IsSpace)
}

// isLineBreak reports whether r ends a line for SplitLines.
func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// SplitLines splits s into lines the way Python's str.splitlines does: at
// every line break character, with "\r\n" counted as one break, and with
// no empty last line when s ends in a break. An empty s has no lines.
func SplitLines(s string) []string {
	var lines []string
	start := 0
	for i, r := range s {
		if !isLineBreak(r) {
			continue
		}
		if r == '\n' && i > 0 && s[i-1] == '\r' {
			start = i + 1
			continue
		}
		lines = append(lines, s[start:i])
		start = i + len(string(r))
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
