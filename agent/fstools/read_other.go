//go:build !linux || android

package fstools

import (
	"context"
	"github.com/Icatme/pi-go/agent"
	"os"
)

func openRegular(_ context.Context, _ agent.ToolExecutionContext, _ *os.Root, _ string) (*os.File, error) {
	return nil, ErrUnsupportedPlatform
}
