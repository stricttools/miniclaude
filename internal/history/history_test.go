package history

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// prompt_toolkit's FileHistory reads testdata/prompt_toolkit_history as
// these entries, oldest first.
var promptToolkitEntries = []string{"first entry", "multi\nline entry\n", "café ✨", "bad � byte"}

func TestParsePromptToolkitMatchesPromptToolkit(t *testing.T) {
	data, err := os.ReadFile("testdata/prompt_toolkit_history")
	if err != nil {
		t.Fatal(err)
	}
	if got := ParsePromptToolkit(data); !slices.Equal(got, promptToolkitEntries) {
		t.Fatalf("got %q", got)
	}
}

func TestRoundTripAndAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "miniclaude", "history.jsonl")
	for _, e := range promptToolkitEntries {
		if err := Append(path, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Load(path)
	if err != nil || !slices.Equal(got, promptToolkitEntries) {
		t.Fatalf("%q %v", got, err)
	}
	info, _ := os.Stat(path)
	dir, _ := os.Stat(filepath.Dir(path))
	if info.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
		t.Errorf("modes %v %v", info.Mode(), dir.Mode())
	}
	data, _ := Marshal([]string{"<html> & co"})
	if string(data) != "{\"text\":\"<html> & co\"}\n" {
		t.Errorf("marshal %q", data)
	}
	if got, err := Load(filepath.Join(t.TempDir(), "missing")); err != nil || got != nil {
		t.Errorf("missing file: %v %v", got, err)
	}
}

func TestUnmarshalRefusals(t *testing.T) {
	for _, bad := range []string{
		`{"text":"a"}`,
		"{\"text\":\"a\",\"x\":1}\n",
		"{\"other\":\"a\"}\n",
		"{\"text\":1}\n",
		"not json\n",
	} {
		if _, err := Unmarshal([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPath(t *testing.T) {
	if p, err := Path("/s"); err != nil || p != "/s/miniclaude/history.jsonl" {
		t.Fatalf("%s %v", p, err)
	}
	if _, err := Path("relative"); err == nil {
		t.Fatal("a relative state directory was accepted")
	}
	t.Setenv("HOME", "/home/x")
	if p, err := Path(""); err != nil || !strings.HasPrefix(p, "/home/x/.local/state/") {
		t.Fatalf("%s %v", p, err)
	}
}
