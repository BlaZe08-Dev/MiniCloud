package scheduler_test

import (
	"errors"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
)

func TestRoundRobinScheduler_Determinism(t *testing.T) {
	sched := scheduler.NewRoundRobinScheduler()

	workers := []*domain.Worker{
		{ID: "worker-b", Status: domain.WorkerStatusHealthy, AvailableSlots: 2, SupportedTasks: []string{"echo"}},
		{ID: "worker-a", Status: domain.WorkerStatusHealthy, AvailableSlots: 1, SupportedTasks: []string{"echo"}},
		{ID: "worker-c", Status: domain.WorkerStatusHealthy, AvailableSlots: 4, SupportedTasks: []string{"echo"}},
	}

	// Sorted alphabetically: worker-a, worker-b, worker-c
	expectedOrder := []string{"worker-a", "worker-b", "worker-c", "worker-a", "worker-b", "worker-c"}

	for i, expectedID := range expectedOrder {
		selected, dec, err := sched.SelectWorker(workers, "echo")
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		if selected.ID != expectedID {
			t.Fatalf("iteration %d: expected %s, got %s", i, expectedID, selected.ID)
		}
		if dec.Selected != expectedID {
			t.Fatalf("iteration %d: decision record selected expected %s, got %s", i, expectedID, dec.Selected)
		}
		if len(dec.Candidates) != 3 {
			t.Fatalf("iteration %d: expected 3 candidates, got %d", i, len(dec.Candidates))
		}
	}
}

func TestRoundRobinScheduler_FiltersIneligible(t *testing.T) {
	sched := scheduler.NewRoundRobinScheduler()

	workers := []*domain.Worker{
		{ID: "worker-full", Status: domain.WorkerStatusHealthy, AvailableSlots: 0, SupportedTasks: []string{"echo"}},
		{ID: "worker-unhealthy", Status: domain.WorkerStatusUnhealthy, AvailableSlots: 2, SupportedTasks: []string{"echo"}},
		{ID: "worker-unsupported", Status: domain.WorkerStatusHealthy, AvailableSlots: 2, SupportedTasks: []string{"sleep"}},
		{ID: "worker-ready", Status: domain.WorkerStatusHealthy, AvailableSlots: 1, SupportedTasks: []string{"echo"}},
	}

	selected, dec, err := sched.SelectWorker(workers, "echo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if selected.ID != "worker-ready" {
		t.Fatalf("expected worker-ready, got %s", selected.ID)
	}
	if len(dec.Candidates) != 1 || dec.Candidates[0] != "worker-ready" {
		t.Fatalf("expected candidates to contain only worker-ready, got %v", dec.Candidates)
	}
}

func TestRoundRobinScheduler_NoWorkersAvailable(t *testing.T) {
	sched := scheduler.NewRoundRobinScheduler()

	workers := []*domain.Worker{
		{ID: "worker-1", Status: domain.WorkerStatusHealthy, AvailableSlots: 0, SupportedTasks: []string{"echo"}},
	}

	_, _, err := sched.SelectWorker(workers, "echo")
	if err == nil {
		t.Fatalf("expected error when no workers have capacity, got nil")
	}
	if !errors.Is(err, domain.ErrNoWorkersAvailable) {
		t.Fatalf("expected ErrNoWorkersAvailable, got %v", err)
	}
}

func TestRoundRobinScheduler_ExcludesUnhealthyWorkers(t *testing.T) {
	sched := scheduler.NewRoundRobinScheduler()

	workers := []*domain.Worker{
		{ID: "worker-unhealthy-1", Status: domain.WorkerStatusUnhealthy, AvailableSlots: 5, SupportedTasks: []string{"echo"}},
		{ID: "worker-unhealthy-2", Status: domain.WorkerStatusUnhealthy, AvailableSlots: 10, SupportedTasks: []string{"echo"}},
	}

	selected, dec, err := sched.SelectWorker(workers, "echo")
	if !errors.Is(err, domain.ErrNoWorkersAvailable) {
		t.Fatalf("expected ErrNoWorkersAvailable for unhealthy workers, got %v (selected=%v)", err, selected)
	}
	if len(dec.Candidates) != 0 {
		t.Fatalf("expected 0 candidates, got %d", len(dec.Candidates))
	}
}
