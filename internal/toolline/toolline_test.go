package toolline

import (
	"encoding/json"
	"os"
	"testing"
)

// call is one formatter call the Python's tool line tests made, with its
// result (scripts/python-expectations toolline-calls).
type call struct {
	Fn     string            `json:"fn"`
	Args   []json.RawMessage `json:"args"`
	Result string            `json:"result"`
}

func TestFormattersMatchThePython(t *testing.T) {
	data, err := os.ReadFile("testdata/python-calls.json")
	if err != nil {
		t.Fatal(err)
	}
	var calls []call
	if err := json.Unmarshal(data, &calls); err != nil {
		t.Fatal(err)
	}
	for i, c := range calls {
		switch c.Fn {
		case "format_tool_use":
			var name string
			var input map[string]any
			_ = json.Unmarshal(c.Args[0], &name)
			_ = json.Unmarshal(c.Args[1], &input)
			subagent := len(c.Args) > 2 && string(c.Args[2]) != "null"
			if got := FormatToolUse(name, input, subagent); got != c.Result {
				t.Errorf("call %d FormatToolUse(%s, %s):\n got: %q\nwant: %q", i, name, c.Args[1], got, c.Result)
			}
		case "format_tool_result":
			var isError bool
			_ = json.Unmarshal(c.Args[2], &isError)
			subagent := len(c.Args) > 3 && string(c.Args[3]) != "null"
			got, err := FormatToolResult(c.Args[1], isError, subagent)
			if err != nil || got != c.Result {
				t.Errorf("call %d FormatToolResult(%s):\n got: %q %v\nwant: %q", i, c.Args[1], got, err, c.Result)
			}
		default:
			t.Fatalf("function %s", c.Fn)
		}
	}
}

func TestFormatToolResultRefusesContentThatIsNotJSON(t *testing.T) {
	if _, err := FormatToolResult(json.RawMessage("{"), false, false); err == nil {
		t.Fatal("invalid JSON content was formatted")
	}
}
