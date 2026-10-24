package agent

import (
	"context"
	"log/slog"

	"github.com/multica-ai/multica/server/pkg/freebuffrt"
)

// Aliases keep freebuff.go readable and let tests fake the orchestrator
// client without importing freebuffrt.
type (
	freebuffThread   = freebuffrt.Thread
	freebuffEvent    = freebuffrt.Event
	freebuffAPIError = freebuffrt.APIError
)

// freebuffClient is the slice of freebuffrt.Client a turn uses.
type freebuffClient interface {
	AuthStatus(ctx context.Context) (freebuffrt.AuthStatus, error)
	Events(ctx context.Context) (<-chan freebuffrt.Event, func() error, error)
	CreateThread(ctx context.Context, projectPath, model string) (freebuffrt.Thread, error)
	GetThread(ctx context.Context, id string) (freebuffrt.Thread, error)
	PostMessage(ctx context.Context, threadID, text string) error
	StopTurn(ctx context.Context, threadID string) error
	HasNotice(ctx context.Context, id, notice string) (bool, error)
	TakeSlot(ctx context.Context, threadID string) error
}

func defaultLogger() *slog.Logger { return slog.Default() }
