package pool

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A fake janis: it answers with its own id, so the test can see which box a call went to.
func fakeJanis(t *testing.T, id string, calls *sync.Map) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n, _ := calls.LoadOrStore(id, new(int))
		*n.(*int)++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"box":"` + id + `","path":"` + r.URL.Path + `","body":` + string(body) + `}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func chat(t *testing.T, h http.Handler, run, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set(HeaderRun, run)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// Memdoor names its run and step; hyperscalr forwards the call to that step's box,
// and every call of a step goes to the same box until the step is done.
func TestChatGoesToTheStepsBox(t *testing.T) {
	var calls sync.Map
	a, b := fakeJanis(t, "a", &calls), fakeJanis(t, "b", &calls)
	r := New(0, 0)
	srv := NewServer(r)
	h := srv.Handler()
	for _, bx := range []struct{ id, url string }{{"a", a.URL}, {"b", b.URL}} {
		r.Add(Member{ID: bx.id, Endpoint: bx.url, ProviderID: "gpu-" + bx.id})
	}
	body := `{"model":"Inkling-2-Mini-4bit","messages":[{"role":"user","content":"hi"}]}`
	code, first := chat(t, h, "run-1", body)
	if code != 200 {
		t.Fatalf("chat: %d %s", code, first)
	}
	if !strings.Contains(first, `"path":"/v1/chat/completions"`) || !strings.Contains(first, `"messages"`) {
		t.Fatalf("the box got the wrong request: %s", first)
	}
	box := strings.Split(strings.Split(first, `"box":"`)[1], `"`)[0]
	for i := 0; i < 4; i++ {
		_, again := chat(t, h, "run-1", body)
		if !strings.Contains(again, `"box":"`+box+`"`) {
			t.Fatalf("the step moved off %s: %s", box, again)
		}
	}
	// the step ends; its box is free
	req := httptest.NewRequest("POST", "/v1/run/done?run=run-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("step done: %d", rec.Code)
	}
}

// A call without the run is refused, not sent to a box.
func TestChatNeedsRunAndStep(t *testing.T) {
	r := New(0, 0)
	h := NewServer(r).Handler()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("no run: %d, want 400", rec.Code)
	}
}

// A box that does not answer is a 502 that names it.
func TestADeadBoxIsA502NamingIt(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := dead.URL
	dead.Close()
	r := New(0, 0)
	r.Add(Member{ID: "gone", Endpoint: url, ProviderID: "gpu-gone"})
	code, body := chat(t, NewServer(r).Handler(), "run-1", "{}")
	if code != 502 || !strings.Contains(body, "gone") {
		t.Fatalf("dead box: %d %s", code, body)
	}
}
