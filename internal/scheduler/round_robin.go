package scheduler

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// RoundRobinScheduler selects workers cyclically from the set of eligible workers.
type RoundRobinScheduler struct {
	mu        sync.Mutex
	lastIndex int
}

// NewRoundRobinScheduler constructs a new deterministic round-robin scheduler.
func NewRoundRobinScheduler() *RoundRobinScheduler {
	return &RoundRobinScheduler{
		lastIndex: -1,
	}
}

func (s *RoundRobinScheduler) Name() string {
	return "round_robin"
}

// SelectWorker filters workers for health, capacity, and task support, sorts them
// deterministically by ID for reproducibility, and advances the round-robin counter.
func (s *RoundRobinScheduler) SelectWorker(workers []*domain.Worker, taskType string) (*domain.Worker, Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	decision := Decision{
		Policy:     s.Name(),
		TaskType:   taskType,
		Candidates: make([]string, 0),
		Timestamp:  time.Now().UTC(),
	}

	// 1. Filter eligible candidates
	var eligible []*domain.Worker
	for _, w := range workers {
		if w.Status == domain.WorkerStatusHealthy && w.AvailableSlots > 0 && w.SupportsTask(taskType) {
			eligible = append(eligible, w)
			decision.Candidates = append(decision.Candidates, w.ID)
		}
	}

	if len(eligible) == 0 {
		decision.Reason = "no healthy worker with available slots supports requested task type"
		slog.Warn("scheduler decision: no workers available",
			"policy", decision.Policy,
			"task_type", decision.TaskType,
			"total_workers", len(workers),
			"reason", decision.Reason,
		)
		return nil, decision, fmt.Errorf("%w: for task type %q", domain.ErrNoWorkersAvailable, taskType)
	}

	// 2. Deterministic sort by Worker ID
	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].ID < eligible[j].ID
	})

	// 3. Round-robin selection
	s.lastIndex = (s.lastIndex + 1) % len(eligible)
	selected := eligible[s.lastIndex]

	decision.Selected = selected.ID
	decision.Reason = fmt.Sprintf("round-robin selected candidate %d of %d eligible", s.lastIndex+1, len(eligible))

	slog.Info("scheduler decision",
		"policy", decision.Policy,
		"task_type", decision.TaskType,
		"candidates", decision.Candidates,
		"selected", decision.Selected,
		"reason", decision.Reason,
	)

	return selected, decision, nil
}
