package domain

import "testing"

// TestReservationRoom_Table: ADR-0020 Section 2.1 and Section 2.4, the predicates at their edges.
// CanReserve is active + reserved < assignable and reserved < MaxReservationsPerWorker;
// HasFallbackRoom drops the cap.
func TestReservationRoom_Table(t *testing.T) {
	m := MaxReservationsPerWorker
	cases := []struct {
		name                         string
		assignable, active, reserved int
		reserve, fallback            bool
	}{
		{"one slot, idle", 1, 0, 0, true, true},
		{"one slot, running", 1, 1, 0, false, false},
		{"one slot, reserved", 1, 0, 1, false, false},
		{"sum one below slots", 4, 1, 2, true, true},
		{"sum equals slots", 4, 2, 2, false, false},
		{"sum above slots (re-registered lower)", 2, 1, 3, false, false},
		{"reserved one below cap", 1024, 0, m - 1, true, true},
		{"reserved at cap", 1024, 0, m, false, true},
		{"reserved at cap, many running", 1024, 500, m, false, true},
		{"active only, below slots", 3, 2, 0, true, true},
	}
	for _, c := range cases {
		if got := CanReserve(c.assignable, c.active, c.reserved); got != c.reserve {
			t.Errorf("%s: CanReserve(%d, %d, %d) = %v, want %v", c.name, c.assignable, c.active, c.reserved, got, c.reserve)
		}
		if got := HasFallbackRoom(c.assignable, c.active, c.reserved); got != c.fallback {
			t.Errorf("%s: HasFallbackRoom(%d, %d, %d) = %v, want %v", c.name, c.assignable, c.active, c.reserved, got, c.fallback)
		}
	}
}

// TestAssignableSlots_UndeclaredIsOne: ADR-0020 Section 2.6, a worker that does not declare
// executes_concurrently has one assignable slot whatever total_slots says; a declared worker has
// total_slots.
func TestAssignableSlots_UndeclaredIsOne(t *testing.T) {
	for n := 1; n <= 1024; n++ {
		if got := AssignableSlots(false, n); got != 1 {
			t.Fatalf("AssignableSlots(false, %d) = %d, want 1", n, got)
		}
		if got := AssignableSlots(true, n); got != n {
			t.Fatalf("AssignableSlots(true, %d) = %d, want %d", n, got, n)
		}
	}
}
