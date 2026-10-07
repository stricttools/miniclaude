package repl

import (
	"context"
	"strings"

	"github.com/stricttools/miniclaude/internal/displaytext"
	"github.com/stricttools/miniclaude/internal/session"
	"github.com/stricttools/miniclaude/internal/usageformat"
)

// helpLines are what /help shows.
var helpLines = []string{
	"/model <name>   switch the model (empty: show current)",
	"/mode <mode>    change permission mode (empty: show current)",
	"/context        show context-window usage",
	"/cost           show session cost and token totals",
	"/quit, /exit    leave the REPL (also Ctrl+D)",
	"/help           show this list",
}

// splitCommand splits a line that starts with "/" into the command (up to
// the first whitespace) and its argument (the rest, trimmed).
func splitCommand(line string) (command, arg string) {
	i := strings.IndexFunc(line, displaytext.IsSpace)
	if i < 0 {
		return line, ""
	}
	return line[:i], displaytext.TrimSpace(line[i:])
}

// slashCommand runs line when it is one of the REPL's own slash commands
// and reports whether it was. Any other line, including an unknown slash
// command, is sent to Claude as typed.
func (c *controller) slashCommand(line string) bool {
	stripped := displaytext.TrimSpace(line)
	if !strings.HasPrefix(stripped, "/") {
		return false
	}
	command, arg := splitCommand(stripped)
	switch command {
	case "/model":
		if arg == "" {
			c.out.Print(dim("model: "+c.modelName()) + "\n")
		} else {
			c.call(func(ctx context.Context, s session.Session) (string, error) {
				return "", s.SetModel(ctx, arg)
			})
		}
	case "/mode":
		if arg == "" {
			c.out.Print(dim("mode: "+c.modeName()) + "\n")
		} else {
			c.call(func(ctx context.Context, s session.Session) (string, error) {
				return "", s.SetPermissionMode(ctx, arg)
			})
		}
	case "/context":
		c.call(func(ctx context.Context, s session.Session) (string, error) {
			usage, err := s.GetContextUsage(ctx)
			if err != nil {
				return "", err
			}
			listing := usageformat.ContextUsage{TotalTokens: usage.TotalTokens, MaxTokens: usage.MaxTokens}
			for _, cat := range usage.Categories {
				listing.Categories = append(listing.Categories, usageformat.ContextCategory{Name: cat.Name, Tokens: cat.Tokens})
			}
			return usageformat.ContextListing(listing), nil
		})
	case "/cost":
		c.out.Print(usageformat.CostLine(c.sess.TotalCostUSD(), c.sess.TotalTokens(), c.sess.TurnCount()))
	case "/quit", "/exit":
		c.exit = true
	case "/help":
		var b strings.Builder
		for _, line := range helpLines {
			b.WriteString(dim(line) + "\n")
		}
		c.out.Print(b.String())
	default:
		return false
	}
	return true
}

// call runs a session call on a goroutine of its own, keeping the REPL busy
// (later lines queue) until it answers; then it shows the text the call
// returned, or its error, and runs the next queued line.
func (c *controller) call(fn func(ctx context.Context, s session.Session) (string, error)) {
	c.busy = true
	sess, base := c.sess, c.base
	updates, stopped := c.updates, c.stopped
	c.goGuarded(func() {
		text, err := fn(base, sess)
		post(updates, stopped, func(c *controller) {
			if err != nil {
				if c.base.Err() == nil {
					c.out.Print(errorLine(err))
				}
			} else {
				c.out.Print(text)
			}
			c.busy = false
			c.next()
		})
	})
}
