package scheduler

import "sort"

// NextAfterCursor is the round-robin rule of ADR-0017 Section 2.3 as a pure function. It returns
// the first candidate after the cursor in byte order, wrapping to the first candidate when none
// is after it. candidates are the eligible worker IDs for one task type, in any order; the rule
// sorts a copy, so the result depends only on the set and the cursor. It reports false when there
// are no candidates.
func NextAfterCursor(cursor string, candidates []string) (string, bool) {
	if len(candidates) == 0 {
		return "", false
	}
	sorted := make([]string, len(candidates))
	copy(sorted, candidates)
	sort.Strings(sorted)
	for _, id := range sorted {
		if id > cursor {
			return id, true
		}
	}
	return sorted[0], true
}

// CursorSet holds one round-robin cursor per task type (ADR-0017 Section 2.3, R1). A type
// missing from the set has the empty cursor. Each decision reads and moves only its own type's
// cursor.
type CursorSet struct {
	last map[string]string
}

// NewCursorSet returns an empty CursorSet.
func NewCursorSet() CursorSet {
	return CursorSet{last: make(map[string]string)}
}

// Select applies NextAfterCursor to the cursor of taskType and records the chosen worker as the
// new cursor for that type. It reports false, and leaves the cursor unchanged, when there are no
// candidates.
func (c *CursorSet) Select(taskType string, candidates []string) (string, bool) {
	next, ok := NextAfterCursor(c.last[taskType], candidates)
	if !ok {
		return "", false
	}
	c.last[taskType] = next
	return next, true
}

// Cursor returns the cursor of taskType, or the empty string when none has been recorded.
func (c CursorSet) Cursor(taskType string) string {
	return c.last[taskType]
}

// SetCursor records the cursor of taskType. It is used to restore cursors read back from storage.
func (c *CursorSet) SetCursor(taskType, workerID string) {
	c.last[taskType] = workerID
}
