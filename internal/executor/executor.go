package executor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

const (
	// MaxPayloadSizeBytes is the hard limit on task input payload (64 KiB).
	MaxPayloadSizeBytes = 64 * 1024

	// MaxOutputSizeBytes is the hard limit on task output (64 KiB).
	MaxOutputSizeBytes = 64 * 1024

	// MaxErrorSizeBytes is the hard limit on an attempt's error text (10 KiB).
	MaxErrorSizeBytes = 10 * 1024

	// TruncationMarker ends any output or error text that was cut to its limit.
	TruncationMarker = "\n[truncated]"

	// DefaultMaxTimeout is the fallback execution timeout if not specified.
	DefaultMaxTimeout = 60 * time.Second
)

// Task defines the interface for an allowlisted task.
type Task interface {
	Name() string
	Validate(payload []byte) error
	Execute(ctx context.Context, payload []byte) (string, error)
}

// Registry manages the set of approved, allowlisted tasks.
type Registry struct {
	mu    sync.RWMutex
	tasks map[string]Task
}

// NewRegistry constructs a new Task registry with the standard allowlisted tasks registered.
func NewRegistry() *Registry {
	r := &Registry{
		tasks: make(map[string]Task),
	}
	r.Register(&EchoTask{})
	r.Register(&SleepTask{})
	r.Register(&SHA256LoopTask{})
	return r
}

// Register adds an approved task to the registry.
func (r *Registry) Register(task Task) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[task.Name()] = task
}

// Get retrieves a task by name.
func (r *Registry) Get(name string) (Task, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	task, exists := r.tasks[name]
	if !exists {
		return nil, fmt.Errorf("%w: %s", domain.ErrUnknownTaskType, name)
	}
	return task, nil
}

// Execute runs the requested task with safety bounds:
//  1. Enforces MaxPayloadSizeBytes.
//  2. Looks up task in allowlist (rejects unknown task types).
//  3. Validates payload structure.
//  4. Enforces execution timeout via context.
//  5. Caps output to MaxOutputSizeBytes on success and failure, cutting at a rune boundary and
//     ending with TruncationMarker (ClampText).
//
// Never runs arbitrary shell commands on the host.
func (r *Registry) Execute(ctx context.Context, taskName string, payload []byte, timeout time.Duration) (string, int, error) {
	if len(payload) > MaxPayloadSizeBytes {
		return "", 1, fmt.Errorf("%w: payload size %d exceeds %d bytes", domain.ErrPayloadTooLarge, len(payload), MaxPayloadSizeBytes)
	}

	task, err := r.Get(taskName)
	if err != nil {
		return "", 1, err
	}

	if err := task.Validate(payload); err != nil {
		return "", 1, fmt.Errorf("%w: %v", domain.ErrInvalidRequest, err)
	}

	if timeout <= 0 {
		timeout = DefaultMaxTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	output, err := task.Execute(execCtx, payload)
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return "", 1, domain.ErrTaskTimeout
		}
		return ClampText(output, MaxOutputSizeBytes), 1, err
	}

	return ClampText(output, MaxOutputSizeBytes), 0, nil
}
