package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// probes answers each call with the next state, the last one from then on.
func probes(states ...setup) func(context.Context) setup {
	i := 0
	return func(context.Context) setup {
		s := states[min(i, len(states)-1)]
		i++
		return s
	}
}

// A SETUP IS WAITED FOR UNTIL ITS SCRIPT SAYS IT IS DONE.
//
// The instance loads, ssh is refused, the script runs two steps, then
// writes the ready file: the wait ends with no error and printed each
// state once.
func TestTheWaitEndsWhenTheSetupIsReady(t *testing.T) {
	var out strings.Builder
	err := waitReady(context.Background(), probes(
		setup{"waiting for the instance:", "loading"},
		setup{"waiting for ssh:", "exit status 255"},
		setup{"setup running:", "building janis"},
		setup{"setup running:", "building janis"},
		setup{"setup running:", "pulling the model"},
		setup{"ready", ""},
	), time.Millisecond, &out)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), "building janis"); n != 1 {
		t.Errorf("a repeated state printed %d times:\n%s", n, out.String())
	}
	if !strings.HasSuffix(out.String(), "setup done\n") {
		t.Errorf("output:\n%s", out.String())
	}
}

// A failed setup ends the wait with the end of its log.
func TestAFailedSetupEndsTheWaitWithItsLog(t *testing.T) {
	err := waitReady(context.Background(), probes(
		setup{"setup running:", "go build"},
		setup{"failed", "ggml.go:12: undefined: foo\nfailed at line 31"},
	), time.Millisecond, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "undefined: foo") {
		t.Fatalf("err = %v, want the log's tail", err)
	}
}

// A setup that never finishes ends the wait at its timeout.
func TestASetupThatNeverEndsTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := waitReady(ctx, probes(setup{"setup running:", "pulling"}), time.Millisecond, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "not done") {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

// The probe's answer is read past the host's ssh banner.
func TestTheProbeIsReadPastTheBanner(t *testing.T) {
	for in, want := range map[string]setup{
		"Welcome to vast.ai.\nHave fun!\nhyperscalr:ready\n":            {"ready", ""},
		"hyperscalr:running\n[ 40%] Building CUDA object\n":             {"setup running:", "[ 40%] Building CUDA object"},
		"Welcome\nhyperscalr:failed\nmake: *** Error 2\nfailed at 31\n": {"failed", "make: *** Error 2\nfailed at 31"},
		"Welcome to vast.ai.\n":                                         {"waiting for ssh:", "no answer"},
	} {
		if got := parseProbe(in); got != want {
			t.Errorf("parseProbe(%q) = %+v, want %+v", in, got, want)
		}
	}
}
