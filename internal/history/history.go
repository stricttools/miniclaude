// Package history reads and writes the REPL's input history: a JSONL file,
// one {"text": ...} object per line, oldest entry first, at
// $XDG_STATE_HOME/miniclaude/history.jsonl ($HOME/.local/state when
// XDG_STATE_HOME is unset). It also parses prompt_toolkit's history file,
// the format the Python REPL kept in ~/.miniclaude/history, for the import
// command, which writes the result through its effects handle with Marshal.
package history

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Entry is one line of the history file.
type Entry struct {
	Text string `json:"text"`
}

// Path returns the history file's path. XDG_STATE_HOME must be absolute
// when it is set; a relative value is refused rather than ignored.
func Path() (string, error) {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("history: finding the home directory: %w", err)
		}
		state = filepath.Join(home, ".local", "state")
	} else if !filepath.IsAbs(state) {
		return "", fmt.Errorf("history: XDG_STATE_HOME is %q, which is not an absolute path", state)
	}
	return filepath.Join(state, "miniclaude", "history.jsonl"), nil
}

// Load returns the entries of the history file at path, oldest first. A
// missing file holds no entries. A line that is not an Entry object is an
// error naming the line.
func Load(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("history: reading %s: %w", path, err)
	}
	entries, err := Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("history: %s: %w", path, err)
	}
	return entries, nil
}

// Unmarshal parses history file bytes into entries, oldest first. Every
// line, the last one included, must end with a newline and hold one Entry
// object with its text field present.
func Unmarshal(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, errors.New("the last line does not end with a newline")
	}
	lines := bytes.Split(data[:len(data)-1], []byte("\n"))
	entries := make([]string, 0, len(lines))
	for i, line := range lines {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		text, ok := raw["text"]
		if !ok || len(raw) != 1 {
			return nil, fmt.Errorf(`line %d: want an object holding only the "text" field`, i+1)
		}
		var s string
		if err := json.Unmarshal(text, &s); err != nil {
			return nil, fmt.Errorf(`line %d: the "text" field: %w`, i+1, err)
		}
		entries = append(entries, s)
	}
	return entries, nil
}

// Marshal returns the history file bytes holding entries, oldest first.
func Marshal(entries []string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, text := range entries {
		if err := enc.Encode(Entry{Text: text}); err != nil {
			return nil, fmt.Errorf("history: encoding an entry: %w", err)
		}
	}
	return buf.Bytes(), nil
}

// Append adds one entry to the end of the history file at path, creating
// the file and its directory when missing. It writes to the filesystem, so
// the caller decides when it runs; it blocks for the length of one small
// write.
func Append(path, text string) (err error) {
	line, err := Marshal([]string{text})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("history: creating %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("history: opening %s: %w", path, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("history: closing %s: %w", path, cerr)
		}
	}()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("history: writing %s: %w", path, err)
	}
	return nil
}

// ParsePromptToolkit parses prompt_toolkit's history file format into
// entries, oldest first: a line starting with "+" holds one line of an
// entry's text after the "+", and any other line (the "#" timestamp line
// before each entry, or a blank line) ends the entry being read. An entry
// is kept when it had at least one "+" line. Bytes that are not UTF-8 are
// replaced with U+FFFD, as prompt_toolkit decodes them.
func ParsePromptToolkit(data []byte) []string {
	text := strings.ToValidUTF8(string(data), "\uFFFD")
	var entries []string
	var lines []string
	inEntry := false
	end := func() {
		if inEntry {
			entries = append(entries, strings.Join(lines, "\n"))
		}
		lines = nil
		inEntry = false
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "+"); ok {
			lines = append(lines, strings.TrimSuffix(rest, "\n"))
			inEntry = true
			continue
		}
		end()
	}
	end()
	return entries
}
