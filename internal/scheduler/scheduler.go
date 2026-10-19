package scheduler

import (
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// Decision records the rationale and inputs for a scheduling action.
type Decision struct {
	Policy     string    `json:"policy"`
	TaskType   string    `json:"task_type"`
	Candidates []string  `json:"candidates"`
	Selected   string    `json:"selected"`
	Reason     string    `json:"reason"`
	Timestamp  time.Time `json:"timestamp"`
}

// Scheduler defines the policy interface for selecting an eligible worker.
type Scheduler interface {
	Name() string
	SelectWorker(workers []*domain.Worker, taskType string) (*domain.Worker, Decision, error)
}
