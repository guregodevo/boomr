package pool

import "time"

// Policy bounds the pool: how many boxes it may hold, and when a box that
// has been idle for IdleFor is drained.
type Policy struct {
	Min, Max int
	IdleFor  time.Duration
}

// Action is what the pool should do now.
type Action struct {
	Add   bool   // rent one more box
	Drain string // drain this box (id), or "" for none
}

// Decide says what to do from the boxes' state. A box at its load bound with
// the pool below Max asks for one more. A box idle for IdleFor, with more
// than Min live boxes, is drained, the longest idle first. idle maps a box id
// to how long it has had no steps.
func Decide(p Policy, members []Member, idle map[string]time.Duration) Action {
	live := 0
	total := 0
	for _, m := range members {
		if !m.Draining {
			live++
			total += m.Active
		}
	}
	var act Action
	if live < p.Max && (live < p.Min || (live > 0 && total >= live)) {
		act.Add = true
	}
	if live > p.Min && p.IdleFor > 0 {
		var best time.Duration
		for _, m := range members {
			if m.Draining || m.Active > 0 {
				continue
			}
			if d, ok := idle[m.ID]; ok && d >= p.IdleFor && d > best {
				best, act.Drain = d, m.ID
			}
		}
	}
	return act
}
