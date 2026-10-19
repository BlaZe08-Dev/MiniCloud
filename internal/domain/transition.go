package domain

// allowedTransitions maps each JobState to the set of valid next states.
var allowedTransitions = map[JobState]map[JobState]bool{
	StateSubmitted: {
		StateQueued:   true,
		StateRejected: true,
	},
	StateQueued: {
		StateRunning:   true,
		StateCancelled: true,
	},
	StateRunning: {
		StateSucceeded:       true,
		StateRetrying:        true,
		StateFailed:          true,
		StateCancelRequested: true,
	},
	StateCancelRequested: {
		StateCancelled: true,
		StateSucceeded: true,
		StateFailed:    true,
	},
	StateRetrying: {
		StateQueued:    true,
		StateFailed:    true,
		StateCancelled: true,
	},
	StateSucceeded: {},
	StateFailed:    {},
	StateCancelled: {},
	StateRejected:  {},
}

// ValidateJobTransition verifies if transitioning from 'current' to 'target' is valid according to the FSM.
// If valid, it returns nil. If invalid, it returns a typed *InvalidStateTransitionError.
func ValidateJobTransition(current, target JobState) error {
	if validTargets, exists := allowedTransitions[current]; exists {
		if validTargets[target] {
			return nil
		}
	}
	return NewInvalidTransitionError(current, target)
}

// IsValidJobTransition checks if the transition from current to target is allowed.
func IsValidJobTransition(current, target JobState) bool {
	return ValidateJobTransition(current, target) == nil
}
