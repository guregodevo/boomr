package pool

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func do(t *testing.T, h http.Handler, method, target, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// The HTTP contract memdoor uses: a box for a run and step, sticky, released.
func TestTheBoxAPI(t *testing.T) {
	r := New(0, 0)
	h := NewServer(r).Handler()
	for _, id := range []string{"a", "b"} {
		if code, body := do(t, h, "POST", "/add", `{"id":"`+id+`","endpoint":"http://`+id+`:8093","provider_id":"gpu-`+id+`"}`); code != 200 {
			t.Fatalf("add %s: %d %s", id, code, body)
		}
	}
	code, body := do(t, h, "GET", "/box?run=run1&step=goal", "")
	if code != 200 {
		t.Fatalf("box: %d %s", code, body)
	}
	var m Member
	json.Unmarshal([]byte(body), &m)
	if m.Endpoint == "" || m.ProviderID == "" {
		t.Fatalf("box without an endpoint or a provider id: %s", body)
	}
	if _, again := do(t, h, "GET", "/box?run=run1&step=goal", ""); again != body {
		t.Errorf("the step moved: %s then %s", body, again)
	}
	if code, _ := do(t, h, "POST", "/release?run=run1&step=goal", ""); code != 200 {
		t.Errorf("release: %d", code)
	}
	if code, _ := do(t, h, "POST", "/release?run=run1&step=goal", ""); code != 404 {
		t.Errorf("a second release: %d, want 404", code)
	}
	if code, _ := do(t, h, "GET", "/box?run=run1", ""); code != 400 {
		t.Errorf("missing step: %d, want 400", code)
	}
	if code, body := do(t, h, "GET", "/ring", ""); code != 200 || !strings.Contains(body, `"members"`) {
		t.Errorf("ring: %d %s", code, body)
	}
}

// A drained box's step stays on it, and removing a box with steps is refused.
func TestTheDrainAPI(t *testing.T) {
	r := New(0, 0)
	h := NewServer(r).Handler()
	do(t, h, "POST", "/add", `{"id":"a","endpoint":"http://a:8093","provider_id":"gpu-a"}`)
	_, body := do(t, h, "GET", "/box?run=run1&step=goal", "")
	var m Member
	json.Unmarshal([]byte(body), &m)
	if code, _ := do(t, h, "POST", "/drain?id="+m.ID, ""); code != 200 {
		t.Fatalf("drain: %d", code)
	}
	if code, _ := do(t, h, "POST", "/remove?id="+m.ID, ""); code != 409 {
		t.Errorf("removing a box with a step: %d, want 409", code)
	}
	do(t, h, "POST", "/release?run=run1&step=goal", "")
	if code, _ := do(t, h, "POST", "/remove?id="+m.ID, ""); code != 200 {
		t.Errorf("removing an empty box: %d", code)
	}
}
