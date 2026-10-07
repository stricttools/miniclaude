// Package dialogs builds what miniclaude shows and answers when Claude asks
// for something: the permission prompt (the text describing the request,
// the menu of choices, and the decision a picked choice makes), the
// AskUserQuestion prompt (its questions, menus, and the answers sent back),
// and the notice for a dialog miniclaude does not support. It holds no
// state and does no I/O: the flow that shows a prompt and waits for the
// user calls these functions and sends the decision through the session.
//
// AskUserQuestion arrives as a permission request and is answered by
// allowing it with the tool input plus an "answers" object mapping each
// question's text to its answer.
package dialogs

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/stricttools/miniclaude/internal/displaytext"
)

const reset = "\x1b[0m"

func sgr(code, text string) string { return "\x1b[" + code + "m" + text + reset }

func bold(text string) string { return sgr("1", text) }

func dim(text string) string { return sgr("2", text) }

func red(text string) string { return sgr("31", text) }

func green(text string) string { return sgr("32", text) }

// Limits on the request description.
const (
	editMaxLines  = 40
	writeMaxLines = 20
	otherMaxChars = 200
)

// Texts the permission and question flows show and send.
const (
	// ChoicePrompt heads the permission menu.
	ChoicePrompt = "Choose an action:"
	// DenialMessagePrompt asks for the message of a denial.
	DenialMessagePrompt = "Denial message: "
	// DeniedByUser is the denial message when the user gives none, and
	// when the user dismisses the permission prompt.
	DeniedByUser = "Denied by user"
	// QuestionDismissedMessage is the denial message when the user
	// dismisses an AskUserQuestion prompt.
	QuestionDismissedMessage = "User dismissed the question."
	// MultiSelectPrompt asks for the options picked in a multiple-choice
	// question.
	MultiSelectPrompt = "Select (comma-separated numbers): "
	// ExtraAnswerPrompt asks for an optional answer of the user's own in a
	// multiple-choice question.
	ExtraAnswerPrompt = "Add your own (optional, blank to skip): "
	// OwnAnswerPrompt asks for the answer when "Other" was picked.
	OwnAnswerPrompt = "Your answer: "
	// OtherLabel labels the last entry of a single-choice menu.
	OtherLabel = "Other (type your own)"
)

// InvalidSelection is shown, followed by asking again, when a multiple-choice
// selection cannot be parsed.
var InvalidSelection = dim("Invalid selection; enter option numbers like 1,3.") + "\n"

// Request is the part of a permission request the prompt reads.
type Request struct {
	ToolName string
	Input    map[string]any
	// Title is the request's own heading, empty when it has none.
	Title string
	// Suggestions are the permission rules offered for "allow always".
	Suggestions []map[string]any
}

// Action is what a permission choice does.
type Action int

const (
	AllowOnce Action = iota
	AllowAlways
	Deny
	DenyWithMessage
)

// Choice is one entry of the permission menu.
type Choice struct {
	Action Action
	Label  string
	// Suggestion is the rule an AllowAlways choice adds.
	Suggestion map[string]any
}

// Decision is the answer to a permission request.
type Decision struct {
	Allow bool
	// UpdatedInput is the tool input sent with an allow.
	UpdatedInput map[string]any
	// UpdatedPermissions are the rules added by an allow, nil for none.
	UpdatedPermissions []map[string]any
	// Message explains a denial.
	Message string
}

// truncate cuts text to limit characters, the last being an ellipsis.
func truncate(text string, limit int) string {
	rs := []rune(text)
	if len(rs) <= limit {
		return text
	}
	return string(rs[:limit-1]) + "…"
}

// SuggestionLabel names a permission suggestion, as in "Bash(git status)"
// or "Bash". It reads the rules of a "rules" array, or of a single "rule"
// object when there is no "rules" array, and falls back to the
// suggestion's compact JSON when no rule names anything.
func SuggestionLabel(suggestion map[string]any) (string, error) {
	var rules []any
	if v, ok := suggestion["rules"]; ok && v != nil {
		if list, isList := v.([]any); isList {
			rules = list
		}
	} else if single, isObject := suggestion["rule"].(map[string]any); isObject {
		rules = []any{single}
	}
	var parts []string
	for _, item := range rules {
		rule, ok := item.(map[string]any)
		if !ok {
			continue
		}
		tool := firstTruthy(rule, "toolName", "tool_name")
		content := firstTruthy(rule, "ruleContent", "rule_content")
		switch {
		case tool != "" && content != "":
			parts = append(parts, tool+"("+content+")")
		case tool != "":
			parts = append(parts, tool)
		case content != "":
			parts = append(parts, content)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, ", "), nil
	}
	compact, err := displaytext.Compact(suggestion)
	if err != nil {
		return "", err
	}
	return truncate(compact, 60), nil
}

// firstTruthy returns the display text of the first of keys whose value is
// truthy in m, or "".
func firstTruthy(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; displaytext.Truthy(v) {
			return displaytext.String(v)
		}
	}
	return ""
}

// PermissionChoices returns the permission menu in order: allow once, one
// "Allow always" per suggestion, deny, and deny with a message. The first
// choice is the default.
func PermissionChoices(req Request) ([]Choice, error) {
	choices := []Choice{{Action: AllowOnce, Label: "Allow once"}}
	for _, s := range req.Suggestions {
		label, err := SuggestionLabel(s)
		if err != nil {
			return nil, err
		}
		choices = append(choices, Choice{Action: AllowAlways, Label: "Allow always: " + label, Suggestion: s})
	}
	choices = append(choices,
		Choice{Action: Deny, Label: "Deny"},
		Choice{Action: DenyWithMessage, Label: "Deny with a message..."},
	)
	return choices, nil
}

// Decide turns the picked choice into the decision for req. message is the
// denial message typed for DenyWithMessage, ignored otherwise; empty means
// DeniedByUser.
func Decide(req Request, choice Choice, message string) Decision {
	switch choice.Action {
	case AllowOnce:
		return Decision{Allow: true, UpdatedInput: req.Input}
	case AllowAlways:
		return Decision{Allow: true, UpdatedInput: req.Input, UpdatedPermissions: []map[string]any{choice.Suggestion}}
	case DenyWithMessage:
		if message == "" {
			message = DeniedByUser
		}
		return Decision{Message: message}
	default:
		return Decision{Message: DeniedByUser}
	}
}

// PermissionDismissed is the decision when the user dismisses the
// permission prompt with Escape or Ctrl+C.
func PermissionDismissed() Decision {
	return Decision{Message: DeniedByUser}
}

// diffSide renders one side of an edit, each line prefixed and colored, up
// to editMaxLines lines and a count of the rest.
func diffSide(text, prefix string, color func(string) string) []string {
	lines := displaytext.SplitLines(text)
	var out []string
	for i, line := range lines {
		if i == editMaxLines {
			break
		}
		out = append(out, color(prefix+line))
	}
	if extra := len(lines) - editMaxLines; extra > 0 {
		out = append(out, moreLines(extra))
	}
	return out
}

func moreLines(extra int) string {
	s := "s"
	if extra == 1 {
		s = ""
	}
	return dim(fmt.Sprintf("  ... (%d more line%s)", extra, s))
}

// inputString returns the display text of input[key], "" when absent.
func inputString(input map[string]any, key string) string {
	v, ok := input[key]
	if !ok {
		return ""
	}
	return displaytext.String(v)
}

// Description renders the text shown above the permission menu, ending in
// a newline: the request's title (or "Claude wants to use <tool>") in bold,
// then the Bash command, the Edit diff in red and green, the Write path,
// size in bytes, and first lines, or for any other tool its input as
// compact JSON.
func Description(req Request) (string, error) {
	header := req.Title
	if header == "" {
		header = "Claude wants to use " + req.ToolName
	}
	lines := []string{bold(header)}
	input := req.Input

	switch req.ToolName {
	case "Bash":
		if command := inputString(input, "command"); command != "" {
			lines = append(lines, command)
		}
	case "Edit":
		if path := firstTruthy(input, "file_path", "path"); path != "" {
			lines = append(lines, dim(path))
		}
		lines = append(lines, diffSide(inputString(input, "old_string"), "- ", red)...)
		lines = append(lines, diffSide(inputString(input, "new_string"), "+ ", green)...)
	case "Write":
		path := firstTruthy(input, "file_path", "path")
		content := inputString(input, "content")
		lines = append(lines, fmt.Sprintf("%s  %d bytes", dim(path), len(content)))
		body := displaytext.SplitLines(content)
		for i, line := range body {
			if i == writeMaxLines {
				break
			}
			lines = append(lines, "  "+line)
		}
		if extra := len(body) - writeMaxLines; extra > 0 {
			lines = append(lines, moreLines(extra))
		}
	default:
		var v any = input
		if input == nil {
			v = map[string]any{}
		}
		compact, err := displaytext.Compact(v)
		if err != nil {
			return "", err
		}
		lines = append(lines, truncate(compact, otherMaxChars))
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// Option is one option of an AskUserQuestion question.
type Option struct {
	Label       string
	Description string
}

// Question is one AskUserQuestion question.
type Question struct {
	Text        string
	MultiSelect bool
	Options     []Option
}

// Questions reads the questions of an AskUserQuestion tool input. A
// missing or null "questions" means none; a "questions" that is not an
// array, or an entry or option that is not an object, is an error.
func Questions(input map[string]any) ([]Question, error) {
	raw, ok := input["questions"]
	if !ok || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("AskUserQuestion input has questions that are not an array: %s", displaytext.String(raw))
	}
	questions := make([]Question, 0, len(list))
	for i, item := range list {
		q, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("AskUserQuestion question %d is not an object: %s", i+1, displaytext.String(item))
		}
		question := Question{
			Text:        inputString(q, "question"),
			MultiSelect: displaytext.Truthy(q["multiSelect"]),
		}
		if opts, isList := q["options"].([]any); isList {
			for j, o := range opts {
				opt, isObject := o.(map[string]any)
				if !isObject {
					return nil, fmt.Errorf("AskUserQuestion question %d option %d is not an object: %s", i+1, j+1, displaytext.String(o))
				}
				option := Option{Label: inputString(opt, "label")}
				if d := opt["description"]; displaytext.Truthy(d) {
					option.Description = displaytext.String(d)
				}
				question.Options = append(question.Options, option)
			}
		} else if opts := q["options"]; displaytext.Truthy(opts) {
			return nil, fmt.Errorf("AskUserQuestion question %d has options that are not an array: %s", i+1, displaytext.String(opts))
		}
		questions = append(questions, question)
	}
	return questions, nil
}

// QuestionHeader is the question's text in bold, ending in a newline.
func QuestionHeader(q Question) string {
	return bold(q.Text) + "\n"
}

// OptionLabel is an option's label, followed by its description in dim when
// it has one that differs from the label.
func OptionLabel(o Option) string {
	if o.Description != "" && o.Description != o.Label {
		return o.Label + "  " + dim(o.Description)
	}
	return o.Label
}

// NumberedOptions lists a multiple-choice question's options numbered from
// 1, one per line, ending in a newline.
func NumberedOptions(q Question) string {
	var b strings.Builder
	for i, o := range q.Options {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, OptionLabel(o))
	}
	if len(q.Options) == 0 {
		b.WriteString("\n")
	}
	return b.String()
}

// SingleChoiceMenu returns the menu labels of a single-choice question: its
// options in order, then OtherLabel. Picking index len(q.Options) means
// "Other"; the first entry is the default.
func SingleChoiceMenu(q Question) []string {
	labels := make([]string, 0, len(q.Options)+1)
	for _, o := range q.Options {
		labels = append(labels, OptionLabel(o))
	}
	return append(labels, OtherLabel)
}

// ParseMultiSelect parses comma-separated option numbers counted from 1
// into indices counted from 0, duplicates dropped and first appearances
// kept in order. It reports false for empty input or any token that is not
// a number from 1 to count, so the caller asks again.
func ParseMultiSelect(text string, count int) ([]int, bool) {
	var tokens []string
	for _, t := range strings.Split(text, ",") {
		if t = displaytext.TrimSpace(t); t != "" {
			tokens = append(tokens, t)
		}
	}
	if len(tokens) == 0 {
		return nil, false
	}
	var idxs []int
	seen := map[int]bool{}
	for _, token := range tokens {
		if strings.Trim(token, "0123456789") != "" {
			return nil, false
		}
		n, err := strconv.Atoi(token)
		if err != nil || n < 1 || n > count {
			return nil, false
		}
		if !seen[n-1] {
			seen[n-1] = true
			idxs = append(idxs, n-1)
		}
	}
	return idxs, true
}

// MultiSelectAnswer is the answer to a multiple-choice question: the labels
// of the picked options, then extra, trimmed, when it is not blank.
func MultiSelectAnswer(q Question, idxs []int, extra string) []string {
	answer := make([]string, 0, len(idxs)+1)
	for _, i := range idxs {
		answer = append(answer, q.Options[i].Label)
	}
	if extra = displaytext.TrimSpace(extra); extra != "" {
		answer = append(answer, extra)
	}
	return answer
}

// SingleChoiceAnswer is the answer to a single-choice question. picked is
// the index chosen in SingleChoiceMenu; when it is the "Other" entry, the
// answer is own, the typed text as it is, and otherwise the picked
// option's label.
func SingleChoiceAnswer(q Question, picked int, own string) []string {
	if picked == len(q.Options) {
		return []string{own}
	}
	return []string{q.Options[picked].Label}
}

// QuestionAnswers is the decision answering an AskUserQuestion request:
// allow with the tool input plus "answers" mapping each question's text to
// its answer, the parts of an answer joined by ", ". answers are matched to
// questions by position; extra on either side are ignored.
func QuestionAnswers(input map[string]any, questions []Question, answers [][]string) Decision {
	byText := make(map[string]any, len(questions))
	for i, q := range questions {
		if i >= len(answers) {
			break
		}
		byText[q.Text] = strings.Join(answers[i], ", ")
	}
	updated := make(map[string]any, len(input)+1)
	for k, v := range input {
		updated[k] = v
	}
	updated["answers"] = byText
	return Decision{Allow: true, UpdatedInput: updated}
}

// QuestionDismissed is the decision when the user dismisses an
// AskUserQuestion prompt with Escape or Ctrl+C.
func QuestionDismissed() Decision {
	return Decision{Message: QuestionDismissedMessage}
}

// DialogNotice is the dim line, ending in a newline, shown when Claude
// opens a dialog miniclaude does not support; the dialog is then
// cancelled. An empty name is shown as "unknown".
func DialogNotice(name string) string {
	if name == "" {
		name = "unknown"
	}
	return dim(fmt.Sprintf("dialog '%s' not supported here; cancelling", name)) + "\n"
}
