package pool

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Server is the HTTP face of a ring: memdoor asks it for a box per step.
//
//	POST /v1/chat/completions   a model call, with the run and step in headers: forwarded to the step's box
//	POST /v1/step/done?run=R&step=S   the step ends: its box is free
//	GET  /box?run=R&step=S   the box for a step, for status (sticky)
//	POST /release?run=R&step=S   the same as /v1/step/done
//	GET  /ring               the boxes and the steps placed now
//	POST /add                a box (JSON Member); a known id is replaced
//	POST /drain?id=B         no new steps on B; its steps finish there
//	POST /remove?id=B        forget B once it has no steps
type Server struct {
	ring *Ring
	mu   sync.Mutex
	idle map[string]time.Time // when each box last had no steps
	now  func() time.Time
}

// NewServer serves ring.
func NewServer(ring *Ring) *Server {
	return &Server{ring: ring, idle: map[string]time.Time{}, now: time.Now}
}

// Handler is the server's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.Chat)
	mux.HandleFunc("POST /v1/step/done", s.StepDone)
	mux.HandleFunc("GET /box", s.box)
	mux.HandleFunc("POST /release", s.release)
	mux.HandleFunc("GET /ring", s.ringState)
	mux.HandleFunc("POST /add", s.add)
	mux.HandleFunc("POST /drain", s.drain)
	mux.HandleFunc("POST /remove", s.remove)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) box(w http.ResponseWriter, r *http.Request) {
	run, step := r.URL.Query().Get("run"), r.URL.Query().Get("step")
	if run == "" || step == "" {
		fail(w, http.StatusBadRequest, errors.New("run and step are needed"))
		return
	}
	m, err := s.ring.Place(run, step)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, err)
		return
	}
	s.mu.Lock()
	delete(s.idle, m.ID)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	run, step := r.URL.Query().Get("run"), r.URL.Query().Get("step")
	if err := s.ring.Release(run, step); err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	s.markIdle()
	writeJSON(w, http.StatusOK, map[string]string{"released": run + "/" + step})
}

func (s *Server) ringState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"members": s.ring.Members(), "steps": s.ring.Steps()})
}

func (s *Server) add(w http.ResponseWriter, r *http.Request) {
	var m Member
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.ring.Add(m); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"added": m.ID})
}

func (s *Server) drain(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if err := s.ring.Drain(id); err != nil {
		fail(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"draining": id})
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if err := s.ring.Remove(id); err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	s.mu.Lock()
	delete(s.idle, id)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"removed": id})
}

// markIdle records when each box last had no steps, for the scale policy.
func (s *Server) markIdle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for _, m := range s.ring.Members() {
		if m.Active == 0 {
			if _, ok := s.idle[m.ID]; !ok {
				s.idle[m.ID] = now
			}
		} else {
			delete(s.idle, m.ID)
		}
	}
}

// IdleFor is how long each box has had no steps.
func (s *Server) IdleFor() map[string]time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]time.Duration{}
	now := s.now()
	for id, since := range s.idle {
		out[id] = now.Sub(since)
	}
	return out
}
