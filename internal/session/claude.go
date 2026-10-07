package session

import (
	"context"

	"github.com/stricttools/claudestream"
)

// Options are what the repl command's flags say about the session.
type Options struct {
	Profile string
	Model   string
	// PermissionMode is the mode the session starts in.
	PermissionMode string
	// Cwd is Claude Code's working directory; empty means this process's.
	Cwd string
	// Resolution is claudestream.NewSession{}, claudestream.ResumeSession
	// with the ID, or claudestream.ContinueLastSession{}.
	Resolution claudestream.SessionResolution
	// ClaudeBinary and ClaudewheelBinary are absolute paths.
	ClaudeBinary      string
	ClaudewheelBinary string
}

// Config is the claudestream configuration for the options. Permission
// prompts reach miniclaude as PermissionRequest events; no dialogs are
// declared, so dialog requests are cancelled with a notice.
func Config(o Options) claudestream.Config {
	return claudestream.Config{
		Model:                o.Model,
		Profile:              o.Profile,
		ClaudeBinary:         o.ClaudeBinary,
		ClaudewheelBinary:    o.ClaudewheelBinary,
		Cwd:                  o.Cwd,
		PermissionMode:       o.PermissionMode,
		InterceptPermissions: true,
		Session:              o.Resolution,
	}
}

// Claude is a Session over a Claude Code subprocess.
type Claude struct {
	s *claudestream.Session
}

var _ Session = (*Claude)(nil)

// Start starts Claude Code with the options' configuration. ctx bounds the
// start only.
func Start(ctx context.Context, o Options) (*Claude, error) {
	s, err := claudestream.Start(ctx, Config(o))
	if err != nil {
		return nil, err
	}
	return &Claude{s: s}, nil
}

// Send sends text as the user's message and opens a turn.
func (c *Claude) Send(ctx context.Context, text string) (EventSource, error) {
	turn, err := c.s.Send(ctx, text)
	if err != nil {
		// A nil *Turn returned as an EventSource would not compare equal
		// to nil.
		return nil, err
	}
	return turn, nil
}

func (c *Claude) Interrupt(ctx context.Context) ([]string, error) {
	return c.s.Interrupt(ctx)
}

func (c *Claude) SetModel(ctx context.Context, model string) error {
	return c.s.SetModel(ctx, &model)
}

func (c *Claude) SetPermissionMode(ctx context.Context, mode string) error {
	return c.s.SetPermissionMode(ctx, mode)
}

func (c *Claude) GetContextUsage(ctx context.Context) (claudestream.ContextUsage, error) {
	return c.s.GetContextUsage(ctx)
}

func (c *Claude) RespondAllow(ctx context.Context, requestID string, input map[string]any, permissions []map[string]any) error {
	return c.s.RespondAllow(ctx, requestID, input, permissions)
}

func (c *Claude) RespondDeny(ctx context.Context, requestID, message string) error {
	return c.s.RespondDeny(ctx, requestID, message)
}

func (c *Claude) RespondDialogCancelled(ctx context.Context, requestID string) error {
	return c.s.RespondDialogCancelled(ctx, requestID)
}

func (c *Claude) ModelName() string { return c.s.Model() }

func (c *Claude) PermissionMode() string { return c.s.PermissionMode() }

func (c *Claude) TotalCostUSD() float64 { return c.s.TotalCostUSD() }

func (c *Claude) TotalTokens() int64 { return c.s.TotalTokens() }

func (c *Claude) TurnCount() int { return c.s.TurnCount() }

// Close stops Claude Code.
func (c *Claude) Close() error { return c.s.Close() }
