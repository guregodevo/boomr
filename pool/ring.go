// Package pool places agent steps on rented boxes: a consistent-hash ring
// over the boxes, with bounded load, sticky until a step is released. Boxes
// may be added or drained at any moment; a step on a drained box stays there
// until it finishes.
package pool

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
)

// Member is one box in the pool.
type Member struct {
	ID         string `json:"id"`          // the rented instance's id
	Endpoint   string `json:"endpoint"`    // janis's base address, http://host:port
	ProviderID string `json:"provider_id"` // the memdoor provider id that names this box, e.g. gpu2
	Draining   bool   `json:"draining"`    // no new steps; the steps it has run on
	Active     int    `json:"active"`      // steps on it now
}

// Assignment is where one step runs.
type Assignment struct {
	Run, Step string
	Member    string
}

// ErrEmpty is returned when no box is live.
var ErrEmpty = errors.New("pool: no box is live")

// Ring places steps on boxes.
type Ring struct {
	mu      sync.Mutex
	vnodes  int
	load    float64 // bounded-load factor c: a box takes at most c times the average
	members map[string]*Member
	points  []point           // the live boxes' virtual nodes, sorted by hash
	sticky  map[string]string // step key -> member id
}

type point struct {
	h  uint64
	id string
}

// New returns an empty ring. vnodes is the virtual nodes per box (150 is
// plenty for a handful of boxes); load is the bounded-load factor (1.25).
func New(vnodes int, load float64) *Ring {
	if vnodes <= 0 {
		vnodes = 150
	}
	if load < 1 {
		load = 1.25
	}
	return &Ring{vnodes: vnodes, load: load, members: map[string]*Member{}, sticky: map[string]string{}}
}

// hash places a key or a virtual node on the ring: SHA-256, whose output is
// uniform even for near-identical strings such as "a#0" and "a#1" (FNV's was
// not, and one box took 3x another's share).
func hash(s string) uint64 {
	sum := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint64(sum[:8])
}

func key(run, step string) string { return run + "\x00" + step }

// Add puts a box on the ring. A box with the same id replaces the old one.
func (r *Ring) Add(m Member) error {
	if m.ID == "" || m.Endpoint == "" {
		return fmt.Errorf("pool: a box needs an id and an endpoint")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.members[m.ID]; ok {
		m.Active = old.Active
	}
	mc := m
	r.members[m.ID] = &mc
	r.rebuild()
	return nil
}

// Drain takes a box out of the ring for new steps. Its steps keep it until
// they are released; Remove destroys its record once it is empty.
func (r *Ring) Drain(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[id]
	if !ok {
		return fmt.Errorf("pool: no box %s", id)
	}
	m.Draining = true
	r.rebuild()
	return nil
}

// Remove forgets a box that has no steps on it.
func (r *Ring) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[id]
	if !ok {
		return fmt.Errorf("pool: no box %s", id)
	}
	if m.Active > 0 {
		return fmt.Errorf("pool: box %s has %d steps", id, m.Active)
	}
	delete(r.members, id)
	r.rebuild()
	return nil
}

// rebuild recomputes the ring from the live boxes. Called with mu held.
func (r *Ring) rebuild() {
	r.points = r.points[:0]
	for _, m := range r.members {
		if m.Draining {
			continue
		}
		for i := 0; i < r.vnodes; i++ {
			r.points = append(r.points, point{h: hash(fmt.Sprintf("%s#%d", m.ID, i)), id: m.ID})
		}
	}
	sort.Slice(r.points, func(i, j int) bool { return r.points[i].h < r.points[j].h })
}

func (r *Ring) live() int {
	n := 0
	for _, m := range r.members {
		if !m.Draining {
			n++
		}
	}
	return n
}

// Place returns the box for a step. A step placed before keeps its box while
// that box exists, drained or not. A new step goes to the first live box
// clockwise from its key that is under the load bound; with every live box
// at its bound, the least loaded one.
func (r *Ring) Place(run, step string) (Member, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(run, step)
	if id, ok := r.sticky[k]; ok {
		if m, ok := r.members[id]; ok {
			return *m, nil
		}
		delete(r.sticky, k) // its box was removed: place it again
	}
	n := r.live()
	if n == 0 {
		return Member{}, ErrEmpty
	}
	total := 0
	for _, m := range r.members {
		total += m.Active
	}
	bound := int(math.Ceil(r.load * float64(total+1) / float64(n)))
	if bound < 1 {
		bound = 1
	}
	h := hash(k)
	start := sort.Search(len(r.points), func(i int) bool { return r.points[i].h >= h })
	var pick *Member
	for i := 0; i < len(r.points); i++ {
		p := r.points[(start+i)%len(r.points)]
		m := r.members[p.id]
		if m.Active < bound {
			pick = m
			break
		}
	}
	if pick == nil { // every live box at the bound: the least loaded
		for _, m := range r.members {
			if !m.Draining && (pick == nil || m.Active < pick.Active || (m.Active == pick.Active && m.ID < pick.ID)) {
				pick = m
			}
		}
	}
	pick.Active++
	r.sticky[k] = pick.ID
	return *pick, nil
}

// Release ends a step: its box is free for another.
func (r *Ring) Release(run, step string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(run, step)
	id, ok := r.sticky[k]
	if !ok {
		return fmt.Errorf("pool: no step %s/%s", run, step)
	}
	delete(r.sticky, k)
	if m, ok := r.members[id]; ok && m.Active > 0 {
		m.Active--
	}
	return nil
}

// Members returns the boxes, sorted by id.
func (r *Ring) Members() []Member {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Member, 0, len(r.members))
	for _, m := range r.members {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Steps returns the steps placed now.
func (r *Ring) Steps() []Assignment {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Assignment, 0, len(r.sticky))
	for k, id := range r.sticky {
		run, step := k, ""
		for i := 0; i < len(k); i++ {
			if k[i] == 0 {
				run, step = k[:i], k[i+1:]
				break
			}
		}
		out = append(out, Assignment{Run: run, Step: step, Member: id})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Run != out[j].Run {
			return out[i].Run < out[j].Run
		}
		return out[i].Step < out[j].Step
	})
	return out
}
