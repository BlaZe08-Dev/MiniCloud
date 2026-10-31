package scheduler_test

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
)

// propertyIterations is the iteration count the ADR requires for INV-4 and INV-4b (Section 6.4).
const propertyIterations = 1000

// randomSorted returns n distinct worker IDs in byte order, drawn from a pool of 10.
func randomSorted(rnd *rand.Rand, n int) []string {
	perm := rnd.Perm(10)[:n]
	ids := make([]string, 0, n)
	for _, p := range perm {
		ids = append(ids, fmt.Sprintf("w_%d", p))
	}
	sort.Strings(ids)
	return ids
}

// INV-4: over K decisions with a constant set S and no removal, each member is selected
// floor(K/|S|) or ceil(K/|S|) times (ADR-0017 Section 2.6, S2).
func TestProperty_RoundRobin_CountsWithinOne(t *testing.T) {
	for seed := int64(1); seed <= propertyIterations; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		set := randomSorted(rnd, 1+rnd.Intn(6))
		cursor := ""
		if rnd.Intn(2) == 0 {
			cursor = set[rnd.Intn(len(set))]
		}
		k := 1 + rnd.Intn(60)
		counts := map[string]int{}
		for i := 0; i < k; i++ {
			w, ok := scheduler.NextAfterCursor(cursor, set)
			if !ok {
				t.Fatalf("seed %d: no selection from a non-empty set", seed)
			}
			counts[w]++
			cursor = w
		}
		lo, hi := k, 0
		for _, id := range set {
			if counts[id] < lo {
				lo = counts[id]
			}
			if counts[id] > hi {
				hi = counts[id]
			}
		}
		if hi-lo > 1 {
			t.Fatalf("seed %d: counts spread %d over %d decisions with %d members: %v", seed, hi-lo, k, len(set), counts)
		}
	}
}

// INV-4b: no double selection while another eligible worker waits, per task type, with task
// types interleaved and eligibility random per decision (ADR-0017 Section 2.6, S1).
//
// For two decisions d1 < d2 of one type that select y, if z != y is eligible at every decision
// of that type in (d1, d2], then z is selected for that type in (d1, d2).
func TestProperty_RoundRobin_NoDoubleSelectionPerType(t *testing.T) {
	types := []string{"echo", "sleep", "scan"}
	for seed := int64(1); seed <= propertyIterations; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		cursors := scheduler.NewCursorSet()
		pool := 2 + rnd.Intn(4)
		var ids []string
		for i := 0; i < pool; i++ {
			ids = append(ids, fmt.Sprintf("w_%d", i))
		}

		type decision struct {
			eligible map[string]bool
			selected string
		}
		history := map[string][]decision{}
		steps := 10 + rnd.Intn(60)
		for i := 0; i < steps; i++ {
			ty := types[rnd.Intn(1+rnd.Intn(len(types)))]
			var set []string
			elig := map[string]bool{}
			for _, id := range ids {
				if rnd.Intn(3) != 0 {
					set = append(set, id)
					elig[id] = true
				}
			}
			if len(set) == 0 {
				continue
			}
			w, ok := cursors.Select(ty, set)
			if !ok {
				t.Fatalf("seed %d: Select on a non-empty set returned no worker", seed)
			}
			history[ty] = append(history[ty], decision{eligible: elig, selected: w})
		}

		for ty, ds := range history {
			for i := range ds {
				for j := i + 1; j < len(ds); j++ {
					if ds[i].selected != ds[j].selected {
						continue
					}
					y := ds[i].selected
					for _, z := range ids {
						if z == y {
							continue
						}
						always := true
						for k := i + 1; k <= j; k++ {
							if !ds[k].eligible[z] {
								always = false
								break
							}
						}
						if !always {
							continue
						}
						found := false
						for k := i + 1; k < j; k++ {
							if ds[k].selected == z {
								found = true
								break
							}
						}
						if !found {
							t.Fatalf("seed %d type %s: %s selected at decisions %d and %d while %s waited eligible throughout", seed, ty, y, i, j, z)
						}
					}
				}
			}
		}
	}
}

// Determinism: the pure rule returns the same answer for the same snapshot, and does not depend
// on the order of the caller's slice beyond its sorted content.
func TestNextAfterCursor_Deterministic(t *testing.T) {
	set := []string{"w_a", "w_b", "w_c"}
	for _, cursor := range []string{"", "w_0", "w_a", "w_b", "w_c", "w_z"} {
		first, ok1 := scheduler.NextAfterCursor(cursor, set)
		second, ok2 := scheduler.NextAfterCursor(cursor, set)
		if first != second || ok1 != ok2 || !ok1 {
			t.Fatalf("cursor %q: %q/%v then %q/%v", cursor, first, ok1, second, ok2)
		}
	}
}

// The cases of the rule: successor after the cursor, wrap to the minimum, and the empty set.
func TestNextAfterCursor_Cases(t *testing.T) {
	set := []string{"w_a", "w_b", "w_c"}
	cases := []struct {
		name   string
		cursor string
		want   string
		ok     bool
	}{
		{"empty cursor selects the minimum", "", "w_a", true},
		{"successor of the cursor", "w_a", "w_b", true},
		{"wraps past the last member", "w_c", "w_a", true},
		{"cursor that left the set moves to its successor", "w_bb", "w_c", true},
		{"empty set has no selection", "w_a", "", false},
	}
	for _, c := range cases {
		s := set
		if c.name == "empty set has no selection" {
			s = nil
		}
		got, ok := scheduler.NextAfterCursor(c.cursor, s)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %q/%v, want %q/%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// Restart: a cursor read back from storage gives the same next selection as the cursor the
// process held before it stopped, for every task type (ADR-0017 Section 2.3, Restart).
func TestCursorSet_RestartContinuesRotation(t *testing.T) {
	set := []string{"w_a", "w_b", "w_c"}
	before := scheduler.NewCursorSet()
	var history []string
	for i := 0; i < 4; i++ {
		w, _ := before.Select("echo", set)
		history = append(history, w)
	}
	w, _ := before.Select("sleep", set)
	history = append(history, w)

	after := scheduler.NewCursorSet()
	after.SetCursor("echo", before.Cursor("echo"))
	after.SetCursor("sleep", before.Cursor("sleep"))

	gotEcho, _ := after.Select("echo", set)
	wantEcho, _ := before.Select("echo", set)
	if wantEcho == "" {
		t.Fatalf("control: the rotation produced no selection")
	}
	if gotEcho != wantEcho {
		t.Fatalf("echo after restart: %q, want %q (history %v)", gotEcho, wantEcho, history)
	}
	if !reflect.DeepEqual(after.Cursor("sleep"), before.Cursor("sleep")) {
		t.Fatalf("sleep cursor diverged after restart")
	}
}
