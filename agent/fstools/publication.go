package fstools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
)

func publicationFailure(operation string, committed, attempted bool, message string, cause error) error {
	reason := operation + "_not_published"
	if attempted {
		reason = operation + "_publication_unknown"
	}
	if committed {
		reason = operation + "_published"
	}
	code := agent.ToolFailureResource
	if errors.Is(cause, context.Canceled) {
		code = agent.ToolFailureCanceled
	} else if errors.Is(cause, context.DeadlineExceeded) {
		code = agent.ToolFailureDeadline
	} else {
		var typed *agent.ToolExecutionError
		if errors.As(cause, &typed) && typed.Code == agent.ToolFailurePolicyDenied {
			code = agent.ToolFailurePolicyDenied
		}
	}
	return &agent.ToolExecutionError{Code: code, Reason: reason, Message: message, Err: cause, Execution: agent.ToolExecutionInfo{Remote: agent.ToolRemoteNotApplicable}}
}
func publicationResult(operation string, synced bool) agent.ToolResult {
	durability := "unknown"
	if synced {
		durability = "synced"
	}
	key := "created"
	if operation != "create" {
		key = "published"
	}
	raw, _ := json.Marshal(map[string]any{key: true, "durability": durability})
	return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: string(raw)}}, StructuredContent: raw, Execution: &agent.ToolExecutionInfo{Remote: agent.ToolRemoteNotApplicable}}
}
func publishFile(ctx context.Context, in agent.ToolExecutionContext, pending pendingCreation, data []byte, operation string) (result agent.ToolResult, err error) {
	committed, attempted, synced := false, false, false
	defer func() {
		if p := recover(); p != nil {
			cause, ok := p.(error)
			if !ok {
				cause = fmt.Errorf("publication panic: %v", p)
			}
			message := "fstools: publication failed unexpectedly; inspect destination before retrying"
			if committed {
				message = "fstools: file was published before an unexpected failure; do not blindly retry or roll back"
			}
			err = publicationFailure(operation, committed, attempted, message, cause)
		}
		closeErr := pending.Close()
		if closeErr != nil {
			if err == nil {
				message := "fstools: publication cleanup failed; inspect destination before retrying"
				if committed {
					message = "fstools: file was published but cleanup failed; do not blindly retry or roll back"
				}
				err = publicationFailure(operation, committed, attempted, message, closeErr)
			} else {
				err = errors.Join(err, publicationFailure(operation, committed, attempted, "fstools: publication cleanup failed", closeErr))
			}
		}
		if committed {
			result = publicationResult(operation, synced)
		}
	}()
	// Staging owns its cleanup. After Publish is attempted, never remove the
	// destination by pathname. A replacement may briefly name its staged inode.
	for len(data) > 0 {
		if err = permission(ctx, in); err != nil {
			return
		}
		end := min(len(data), 8192)
		var n int
		n, err = pending.Write(data[:end])
		if err != nil {
			err = publicationFailure(operation, false, false, "fstools: cannot stage file content; new content was not published", err)
			return
		}
		if n < 1 || n > end {
			err = publicationFailure(operation, false, false, "fstools: file staging made invalid progress; new content was not published", io.ErrShortWrite)
			return
		}
		data = data[n:]
	}
	if err = permission(ctx, in); err != nil {
		return
	}
	if err = pending.Sync(); err != nil {
		err = publicationFailure(operation, false, false, "fstools: cannot sync staged file; new content was not published", err)
		return
	}
	if err = permission(ctx, in); err != nil {
		return
	}
	attempted = true
	if err = pending.Publish(); err != nil {
		if operation == "create" && errors.Is(err, os.ErrExist) {
			err = publicationFailure(operation, false, false, "fstools: destination already exists; no overwrite performed", err)
			return
		}
		err = publicationFailure(operation, false, true, "fstools: file publication was not confirmed; inspect destination before retrying", err)
		return
	}
	committed = true
	if !executionreceipt.ReportLocalCommit(ctx) {
		err = publicationFailure(operation, true, true, "fstools: file was published but receipt reporting failed; inspect before retrying", errors.New("receipt capture unavailable"))
		return
	}
	// Publication is irreversible here. Revocation/cancellation changes reporting,
	// not the committed fact, and cannot authorize unlinking a replaced pathname.
	if err = permission(ctx, in); err != nil {
		err = publicationFailure(operation, true, true, "fstools: file was published before cancellation or revocation; durability unknown", err)
		return
	}
	if err = pending.SyncDirectory(); err != nil {
		err = publicationFailure(operation, true, true, "fstools: file was published but directory sync failed; durability unknown", err)
		return
	}
	synced = true
	if err = permission(ctx, in); err != nil {
		err = publicationFailure(operation, true, true, "fstools: file was published and synced before cancellation or revocation", err)
		return
	}
	return
}
