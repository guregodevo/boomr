package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guregodevo/boostr/pool"
)

func janisStub(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestPoolAddPutsTheBoxInTheRingOnceAndAgainIsANoOp(t *testing.T) {
	ring := pool.New(0, 0)
	p := httptest.NewServer(pool.NewServer(ring).Handler())
	defer p.Close()
	janis := janisStub(t)
	tunnels := 0
	tunnel := func() (string, error) { tunnels++; return janis.URL, nil }

	var out bytes.Buffer
	for i := 0; i < 2; i++ {
		if err := addBox(context.Background(), p.URL, "42", tunnel, &out); err != nil {
			t.Fatal(err)
		}
	}
	if tunnels != 1 {
		t.Fatalf("tunnels opened: %d, want 1 (the second add is a no-op)", tunnels)
	}
	if !strings.Contains(out.String(), "already in the pool") {
		t.Fatalf("second add said: %q", out.String())
	}
	st, err := fetchRing(context.Background(), p.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Members) != 1 || st.Members[0].ID != "42" || st.Members[0].Endpoint != janis.URL {
		t.Fatalf("ring: %+v", st.Members)
	}
}

func TestPoolAddReplacesABoxWhoseEndpointNoLongerAnswers(t *testing.T) {
	ring := pool.New(0, 0)
	p := httptest.NewServer(pool.NewServer(ring).Handler())
	defer p.Close()
	dead := janisStub(t)
	if err := ring.Add(pool.Member{ID: "42", Endpoint: dead.URL}); err != nil {
		t.Fatal(err)
	}
	dead.Close()
	janis := janisStub(t)
	var out bytes.Buffer
	if err := addBox(context.Background(), p.URL, "42", func() (string, error) { return janis.URL, nil }, &out); err != nil {
		t.Fatal(err)
	}
	st, _ := fetchRing(context.Background(), p.URL)
	if len(st.Members) != 1 || st.Members[0].Endpoint != janis.URL {
		t.Fatalf("ring: %+v", st.Members)
	}
}

func TestPoolAddRefusesABoxWhoseJanisDoesNotAnswer(t *testing.T) {
	old := healthWithin
	healthWithin = 0
	defer func() { healthWithin = old }()
	p := httptest.NewServer(pool.NewServer(pool.New(0, 0)).Handler())
	defer p.Close()
	dead := janisStub(t)
	dead.Close()
	err := addBox(context.Background(), p.URL, "42", func() (string, error) { return dead.URL, nil }, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "janis does not answer") {
		t.Fatalf("err = %v", err)
	}
}
