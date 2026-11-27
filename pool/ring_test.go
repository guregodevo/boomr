package pool

import (
	"fmt"
	"testing"
	"time"
)

func boxes(r *Ring, ids ...string) {
	for _, id := range ids {
		r.Add(Member{ID: id, Endpoint: "http://" + id + ":8093", ProviderID: "gpu-" + id})
	}
}

// A step goes to the same box every time it is asked for.
func TestAStepStaysOnItsBox(t *testing.T) {
	r := New(0, 0)
	boxes(r, "a", "b", "c")
	first, err := r.Place("run1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, _ := r.Place("run1")
		if again.ID != first.ID {
			t.Fatalf("step moved from %s to %s", first.ID, again.ID)
		}
	}
}

// Steps spread over the boxes, close to evenly, with the load bound on.
func TestStepsSpreadOverTheBoxes(t *testing.T) {
	r := New(0, 0)
	boxes(r, "a", "b", "c", "d")
	counts := map[string]int{}
	for i := 0; i < 400; i++ {
		m, err := r.Place(fmt.Sprintf("run%d", i))
		if err != nil {
			t.Fatal(err)
		}
		counts[m.ID]++
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		if counts[id] < 80 || counts[id] > 120 {
			t.Errorf("box %s got %d of 400 steps, want about 100", id, counts[id])
		}
	}
}

// Adding a box moves only the steps the new box takes over, about 1/N.
func TestAddingABoxMovesAboutAQuarter(t *testing.T) {
	r := New(0, 0)
	boxes(r, "a", "b", "c")
	before := map[string]string{}
	for i := 0; i < 300; i++ {
		m, _ := r.Place(fmt.Sprintf("run%d", i))
		before[fmt.Sprintf("run%d", i)] = m.ID
		r.Release(fmt.Sprintf("run%d", i)) // unload, so only the ring decides
	}
	boxes(r, "d")
	moved := 0
	for i := 0; i < 300; i++ {
		m, _ := r.Place(fmt.Sprintf("run%d", i))
		if m.ID != before[fmt.Sprintf("run%d", i)] {
			moved++
			if m.ID != "d" {
				t.Errorf("a step moved to %s, not to the new box", m.ID)
			}
		}
		r.Release(fmt.Sprintf("run%d", i))
	}
	if moved < 40 || moved > 110 {
		t.Errorf("%d of 300 steps moved to the new box, want about 75", moved)
	}
}

// A step placed before a box was drained stays on it until it is released.
func TestADrainedBoxKeepsItsSteps(t *testing.T) {
	r := New(0, 0)
	boxes(r, "a", "b")
	m, _ := r.Place("run1")
	if err := r.Drain(m.ID); err != nil {
		t.Fatal(err)
	}
	again, err := r.Place("run1")
	if err != nil || again.ID != m.ID {
		t.Fatalf("drained box lost its step: %v %v", again.ID, err)
	}
	if err := r.Remove(m.ID); err == nil {
		t.Fatal("removed a box with a step on it")
	}
	r.Release("run1")
	if err := r.Remove(m.ID); err != nil {
		t.Fatalf("empty box not removed: %v", err)
	}
	// new steps do not go to the drained box
	for i := 0; i < 20; i++ {
		p, _ := r.Place(fmt.Sprintf("new%d", i))
		if p.ID == m.ID {
			t.Fatal("a new step went to a drained box")
		}
	}
}

// Bounded load: no box takes more than about the bound while another has room.
func TestBoundedLoadCapsOneBox(t *testing.T) {
	r := New(0, 1.25)
	boxes(r, "a", "b", "c")
	max := 0
	for i := 0; i < 60; i++ {
		m, _ := r.Place(fmt.Sprintf("run%d", i))
		if m.Active > max {
			max = m.Active
		}
	}
	for _, m := range r.Members() {
		if m.Active > max {
			max = m.Active
		}
	}
	if max > 25 { // 60 steps over 3 boxes at 1.25x the average: 25 at most
		t.Errorf("a box took %d steps; the bound allows 25", max)
	}
}

func TestNoBoxIsAnError(t *testing.T) {
	r := New(0, 0)
	if _, err := r.Place("run1"); err != ErrEmpty {
		t.Fatalf("got %v, want ErrEmpty", err)
	}
}

func TestDecide(t *testing.T) {
	p := Policy{Min: 1, Max: 4, IdleFor: 10 * time.Minute}
	busy := []Member{{ID: "a", Active: 1}, {ID: "b", Active: 1}}
	if a := Decide(p, busy, nil); !a.Add {
		t.Error("every box busy: want one more")
	}
	if a := Decide(Policy{Min: 1, Max: 2}, busy, nil); a.Add {
		t.Error("at the maximum: no more")
	}
	idle := []Member{{ID: "a"}, {ID: "b", Active: 1}}
	a := Decide(p, idle, map[string]time.Duration{"a": 11 * time.Minute})
	if a.Drain != "a" || a.Add {
		t.Errorf("idle box over the limit: got %+v, want drain a", a)
	}
	if a := Decide(p, []Member{{ID: "a"}}, map[string]time.Duration{"a": time.Hour}); a.Drain != "" {
		t.Error("the last box is not drained")
	}
	if a := Decide(Policy{Min: 3, Max: 4}, []Member{{ID: "a"}}, nil); !a.Add {
		t.Error("below the minimum: add")
	}
}
