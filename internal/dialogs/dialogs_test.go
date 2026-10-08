package dialogs

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// call is one call of a dialog builder the Python's dialog tests made, with
// its result, recorded by a generator deleted with the Python.
type call struct {
	Fn     string            `json:"fn"`
	Args   []json.RawMessage `json:"args"`
	Result json.RawMessage   `json:"result"`
}

type pythonRequest struct {
	ToolName    string           `json:"tool_name"`
	ToolInput   map[string]any   `json:"tool_input"`
	Title       string           `json:"title"`
	Suggestions []map[string]any `json:"permission_suggestions"`
}

func (r pythonRequest) request() Request {
	return Request{ToolName: r.ToolName, Input: r.ToolInput, Title: r.Title, Suggestions: r.Suggestions}
}

var actionNames = map[Action]string{AllowOnce: "allow_once", AllowAlways: "allow_always", Deny: "deny", DenyWithMessage: "deny_message"}

func TestBuildersMatchThePython(t *testing.T) {
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
		case "suggestion_label":
			var s map[string]any
			_ = json.Unmarshal(c.Args[0], &s)
			var want string
			_ = json.Unmarshal(c.Result, &want)
			if got, err := SuggestionLabel(s); err != nil || got != want {
				t.Errorf("call %d SuggestionLabel(%s): %q %v, want %q", i, c.Args[0], got, err, want)
			}
		case "build_permission_choices":
			var r pythonRequest
			_ = json.Unmarshal(c.Args[0], &r)
			var want []struct {
				Action     string         `json:"action"`
				Label      string         `json:"label"`
				Suggestion map[string]any `json:"suggestion"`
			}
			_ = json.Unmarshal(c.Result, &want)
			got, err := PermissionChoices(r.request())
			if err != nil || len(got) != len(want) {
				t.Errorf("call %d PermissionChoices: %+v %v, want %+v", i, got, err, want)
				continue
			}
			for j := range got {
				if actionNames[got[j].Action] != want[j].Action || got[j].Label != want[j].Label || !reflect.DeepEqual(got[j].Suggestion, want[j].Suggestion) {
					t.Errorf("call %d choice %d: %+v, want %+v", i, j, got[j], want[j])
				}
			}
		case "build_decision_surface":
			var r pythonRequest
			_ = json.Unmarshal(c.Args[0], &r)
			var want string
			_ = json.Unmarshal(c.Result, &want)
			if got, err := Description(r.request()); err != nil || !sameDescription(got, want) {
				t.Errorf("call %d Description(%s):\n got: %q %v\nwant: %q", i, c.Args[0], got, err, want)
			}
		case "build_question_answers":
			var input map[string]any
			_ = json.Unmarshal(c.Args[0], &input)
			var selections []any
			_ = json.Unmarshal(c.Args[1], &selections)
			questions, err := Questions(input)
			if err != nil {
				t.Errorf("call %d Questions: %v", i, err)
				continue
			}
			answers := make([][]string, len(selections))
			for k, s := range selections {
				switch v := s.(type) {
				case string:
					answers[k] = []string{v}
				case []any:
					for _, item := range v {
						answers[k] = append(answers[k], item.(string))
					}
				}
			}
			var want map[string]any
			_ = json.Unmarshal(c.Result, &want)
			got := QuestionAnswers(input, questions, answers)
			gotJSON, _ := json.Marshal(got.UpdatedInput)
			wantJSON, _ := json.Marshal(want)
			if !got.Allow || string(gotJSON) != string(wantJSON) {
				t.Errorf("call %d QuestionAnswers:\n got: %s\nwant: %s", i, gotJSON, wantJSON)
			}
		case "parse_multiselect":
			var text string
			var count int
			_ = json.Unmarshal(c.Args[0], &text)
			_ = json.Unmarshal(c.Args[1], &count)
			var want []int
			_ = json.Unmarshal(c.Result, &want)
			got, ok := ParseMultiSelect(text, count)
			if ok != (string(c.Result) != "null") || !reflect.DeepEqual(got, want) {
				t.Errorf("call %d ParseMultiSelect(%q, %d): %v %v, want %s", i, text, count, got, ok, c.Result)
			}
		default:
			t.Fatalf("function %s", c.Fn)
		}
	}
}

// sameDescription compares two descriptions line by line, a line holding a
// JSON object by its decoded value: the input reaches Go as a map, which
// keeps no key order, so the keys come out sorted (see the deviations log).
func sameDescription(got, want string) bool {
	if got == want {
		return true
	}
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	if len(g) != len(w) {
		return false
	}
	for i := range g {
		if g[i] == w[i] {
			continue
		}
		var gv, wv map[string]any
		if json.Unmarshal([]byte(g[i]), &gv) != nil || json.Unmarshal([]byte(w[i]), &wv) != nil || !reflect.DeepEqual(gv, wv) {
			return false
		}
	}
	return true
}
