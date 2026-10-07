// Package session defines the session miniclaude's controller talks to,
// and its implementation over a Claude Code subprocess run by claudestream.
// The mock package provides the other implementation.
package session

import (
	"context"

	"github.com/stricttools/claudestream"
)

// EventSource yields one turn's events. Next returns io.EOF once the turn
// has ended, and keeps returning it.
type EventSource interface {
	Next(ctx context.Context) (claudestream.Event, error)
}

// Session is a conversation with Claude, one turn at a time. Send opens a
// turn, whose EventSource must be read to io.EOF before the next Send. A
// PermissionRequest is answered with RespondAllow or RespondDeny, and a
// UserDialogRequest with RespondDialogCancelled, while the turn is read.
// The methods are safe for concurrent use.
type Session interface {
	Send(ctx context.Context, text string) (EventSource, error)
	// Interrupt stops the running turn and returns the user messages
	// still queued.
	Interrupt(ctx context.Context) ([]string, error)
	SetModel(ctx context.Context, model string) error
	SetPermissionMode(ctx context.Context, mode string) error
	GetContextUsage(ctx context.Context) (claudestream.ContextUsage, error)
	// RespondAllow allows a permission request; input is the tool input
	// to run with, and permissions, when not nil, are rules to add.
	RespondAllow(ctx context.Context, requestID string, input map[string]any, permissions []map[string]any) error
	// RespondDeny denies a permission request; message is the reason the
	// model sees.
	RespondDeny(ctx context.Context, requestID, message string) error
	// RespondDialogCancelled cancels a dialog request.
	RespondDialogCancelled(ctx context.Context, requestID string) error
	// ModelName is the session's model; empty when not known yet.
	ModelName() string
	PermissionMode() string
	// TotalCostUSD is the session's cost so far.
	TotalCostUSD() float64
	// TotalTokens is the session's input plus output tokens so far.
	TotalTokens() int64
	// TurnCount counts the turns that ended with a result.
	TurnCount() int
	Close() error
}
