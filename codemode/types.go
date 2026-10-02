// Package codemode runs bounded JavaScript orchestration in an isolated
// QuickJS WASM instance. The caller owns tool authorization and side effects.
package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Config bounds guest and host resources. Start with DefaultConfig and change
// individual fields. Memory/count/byte limits must be positive; Timeout alone
// may be zero, leaving cancellation/deadlines to the caller and source header.
type Config struct {
	HeapBytes          uint64
	MemoryLimitPages   uint32
	StackBytes         uint64
	MaxConcurrentRuns  int
	MaxConcurrentCalls int
	MaxCalls           int
	MaxQueue           int
	MaxCodeBytes       int
	MaxArgumentBytes   int
	MaxResultBytes     int
	MaxCompletionBytes int
	MaxCatalogBytes    int
	MaxOutputBytes     int
	MaxOutputItems     int
	MaxOutputTokens    int
	MaxStoreBytes      int
	MaxErrorRecords    int
	MaxErrorBytes      int
	Timeout            time.Duration
}

func DefaultConfig() Config {
	return Config{HeapBytes: 64 << 20, MemoryLimitPages: 2048, StackBytes: 512 << 10,
		MaxConcurrentRuns: 4, MaxConcurrentCalls: 16, MaxCalls: 1024, MaxQueue: 1024,
		MaxCodeBytes: 256 << 10, MaxArgumentBytes: 64 << 10, MaxResultBytes: 1 << 20,
		MaxCompletionBytes: 16 << 20, MaxCatalogBytes: 1 << 20,
		MaxOutputBytes: 32 << 10, MaxOutputItems: 256, MaxOutputTokens: 8000,
		MaxStoreBytes: 1 << 20, MaxErrorRecords: 256, MaxErrorBytes: 32 << 10}
}

func (c Config) validate() error {
	if c.HeapBytes < 1<<20 || c.HeapBytes > 1<<30 || c.MemoryLimitPages < 32 || c.MemoryLimitPages > 32768 || c.HeapBytes > uint64(c.MemoryLimitPages)*65536 {
		return fmt.Errorf("codemode: invalid heap or linear memory limit")
	}
	if c.StackBytes == 0 || c.StackBytes > 512<<10 {
		return fmt.Errorf("codemode: StackBytes must be positive and <= 512 KiB")
	}
	for name, n := range map[string]int{
		"MaxConcurrentRuns": c.MaxConcurrentRuns, "MaxConcurrentCalls": c.MaxConcurrentCalls,
		"MaxCalls": c.MaxCalls, "MaxQueue": c.MaxQueue, "MaxCodeBytes": c.MaxCodeBytes,
		"MaxArgumentBytes": c.MaxArgumentBytes, "MaxResultBytes": c.MaxResultBytes,
		"MaxCompletionBytes": c.MaxCompletionBytes, "MaxCatalogBytes": c.MaxCatalogBytes,
		"MaxOutputBytes": c.MaxOutputBytes, "MaxOutputItems": c.MaxOutputItems,
		"MaxOutputTokens": c.MaxOutputTokens, "MaxStoreBytes": c.MaxStoreBytes,
		"MaxErrorRecords": c.MaxErrorRecords, "MaxErrorBytes": c.MaxErrorBytes,
	} {
		if n <= 0 || n > 64<<20 {
			return fmt.Errorf("codemode: invalid %s", name)
		}
	}
	if c.MaxConcurrentRuns > 64 || c.MaxConcurrentCalls > 16 || c.MaxCalls > 1024 || c.MaxQueue > c.MaxCalls || c.MaxOutputItems > 1024 || c.MaxErrorRecords > 256 || c.Timeout < 0 || c.Timeout > 24*time.Hour {
		return fmt.Errorf("codemode: invalid concurrency, call, output or timeout bound")
	}
	return nil
}

type Tool struct {
	Name         string
	Description  string
	Namespace    string
	Parameters   json.RawMessage
	OutputSchema json.RawMessage
	// Invoke receives a host-generated admission ID. It must respect context.
	// Returned bytes transfer ownership to the sandbox. No automatic retry occurs.
	Invoke func(context.Context, HostCall) (json.RawMessage, error)
}

type HostCall struct {
	ID        int
	Name      string
	Arguments json.RawMessage
}

type RunOptions struct {
	Tools           []Tool
	Timeout         time.Duration
	MaxOutputTokens int
	// Sequential dispatches all calls in admission order, including Promise.all.
	// The next Invoke cannot begin until the previous Invoke actually returns.
	Sequential bool
	// Store is invocation-local state. Each script reads one revision and
	// atomically commits only on success; nil isolates state to this script.
	Store *Store
}

type Output struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

type Execution struct {
	Local  string `json:"local"`
	Remote string `json:"remote"`
}

// CallError is a machine-readable host failure. A tool-declared business
// failure belongs in a successful result envelope, rather than this error.
type CallError struct {
	Code       string    `json:"code"`
	ReasonCode string    `json:"reasonCode,omitempty"`
	Message    string    `json:"message"`
	CallID     string    `json:"callId,omitempty"`
	Execution  Execution `json:"execution"`
	Err        error     `json:"-"`
}

func (e *CallError) Error() string { return e.Message }
func (e *CallError) Unwrap() error { return e.Err }

type CallRecord struct {
	ID         int       `json:"id"`
	Name       string    `json:"name"`
	Code       string    `json:"code,omitempty"`
	ReasonCode string    `json:"reasonCode,omitempty"`
	Execution  Execution `json:"execution"`
}

type Result struct {
	Outputs []Output
	// All accepted calls have a basic record. Arguments/results are not retained.
	Calls []CallRecord
}

type ScriptError struct {
	Code, Message string
	Err           error
}

func (e *ScriptError) Error() string { return "codemode " + e.Code + ": " + e.Message }
func (e *ScriptError) Unwrap() error { return e.Err }

type OutstandingCall struct {
	RunID     uint64
	ID        int
	Name      string
	Execution Execution
}

// CloseError reports hosts that have not exited when the supplied close
// context expires. Their tickets remain occupied until they actually exit.
type CloseError struct {
	Outstanding []OutstandingCall
	Err         error
}

func (e *CloseError) Error() string {
	return fmt.Sprintf("codemode: close incomplete: %d outstanding calls: %v", len(e.Outstanding), e.Err)
}
func (e *CloseError) Unwrap() error { return e.Err }
