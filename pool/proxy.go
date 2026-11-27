package pool

import (
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// The headers memdoor sends with each model call: the run and the step.
const (
	HeaderRun  = "X-Memdoor-Run"
	HeaderStep = "X-Memdoor-Step"
)

// Chat forwards an OpenAI-style chat request to the box the ring gives its
// step, and streams the reply back. Memdoor talks only to boostr: it names
// its run and step, and boostr chooses the box.
func (s *Server) Chat(w http.ResponseWriter, r *http.Request) {
	run, step := r.Header.Get(HeaderRun), r.Header.Get(HeaderStep)
	if run == "" || step == "" {
		fail(w, http.StatusBadRequest, errors.New(HeaderRun+" and "+HeaderStep+" are needed"))
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
	target, err := url.Parse(m.Endpoint)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = "/v1/chat/completions"
			pr.Out.Host = target.Host
		},
		FlushInterval: -1, // a streamed reply goes out as it comes
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// The box did not answer: the step stays placed, and memdoor may
			// ask again; the reply says which box and why.
			fail(w, http.StatusBadGateway, errors.New("box "+m.ID+": "+err.Error()))
		},
	}
	proxy.ServeHTTP(w, r)
}

// StepDone ends a step: its box is free for another, and the next call for
// that step is placed again.
func (s *Server) StepDone(w http.ResponseWriter, r *http.Request) {
	s.release(w, r)
}
