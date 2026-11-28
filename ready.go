package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/guregodevo/hyperscalr/vast"
)

// A setup script run at start (--onstart) says how it went in files on the
// box: readyFile when it is done, failedFile when it stopped on an error,
// its output in setupLog. examples/janis-memdoor.sh writes them.
const defaultSetupTimeout = time.Hour

const (
	readyFile  = "/root/hyperscalr-ready"
	failedFile = "/root/hyperscalr-failed"
	setupLog   = "/root/hyperscalr-setup.log"
)

// setup is what a box says of its setup script: state "ready", "failed" or
// a step still waited on, and detail (the log's last line, or its tail on
// a failure).
type setup struct{ state, detail string }

// waitReady asks probe every interval until the setup is ready (nil),
// failed (an error with the log's tail), or ctx ends. Each new state or log
// line is printed once.
func waitReady(ctx context.Context, probe func(context.Context) setup, every time.Duration, out io.Writer) error {
	last := setup{}
	for {
		s := probe(ctx)
		switch s.state {
		case "ready":
			fmt.Fprintln(out, "setup done")
			if s.detail != "" {
				fmt.Fprintln(out, s.detail)
			}
			return nil
		case "failed":
			return fmt.Errorf("the setup failed; the end of %s:\n%s", setupLog, s.detail)
		}
		if s != last {
			fmt.Fprintf(out, "  %s %s\n", s.state, s.detail)
			last = s
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the setup is not done: %w (hyperscalr ssh ID tail %s)", ctx.Err(), setupLog)
		case <-time.After(every):
		}
	}
}

// probeBox reads instance id's setup state: the instance's state until it
// runs, then the marker files over ssh.
func probeBox(c *vast.Client, id int64) func(context.Context) setup {
	return func(ctx context.Context) setup {
		in, err := c.Instance(ctx, id)
		if err != nil {
			return setup{"waiting for vast.ai:", err.Error()}
		}
		host, port, ok := in.SSH()
		if !in.Running() || !ok {
			return setup{"waiting for the instance:", in.ActualStatus}
		}
		script := fmt.Sprintf("if [ -f %[1]s ]; then echo hyperscalr:ready; cat /root/hyperscalr-manifest 2>/dev/null; elif [ -f %[2]s ]; then echo hyperscalr:failed; tail -n 20 %[3]s; else echo hyperscalr:running; tail -n 1 %[3]s 2>/dev/null; fi", readyFile, failedFile, setupLog)
		b, err := exec.CommandContext(ctx, "ssh", "-p", port, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=15", "root@"+host, script).Output()
		if err != nil {
			return setup{"waiting for ssh:", strings.TrimSpace(fmt.Sprint(err))}
		}
		return parseProbe(string(b))
	}
}

// parseProbe reads probeBox's output, past whatever the host's ssh banner
// put before it.
func parseProbe(s string) setup {
	_, rest, ok := strings.Cut(s, "hyperscalr:")
	if !ok {
		return setup{"waiting for ssh:", "no answer"}
	}
	state, detail, _ := strings.Cut(rest, "\n")
	detail = strings.TrimSpace(detail)
	if state == "running" {
		return setup{"setup running:", detail}
	}
	return setup{state, detail}
}

// waitSetup waits up to timeout for instance id's setup, past the run's
// own deadline.
func waitSetup(ctx context.Context, c *vast.Client, id int64, timeout time.Duration, out io.Writer) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	return waitReady(ctx, probeBox(c, id), 15*time.Second, out)
}

// waitCmd is `hyperscalr wait ID [--timeout D]`: an instance rented or started
// again, until its setup script is done.
func waitCmd(ctx context.Context, c *vast.Client, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: hyperscalr wait ID [--timeout 1h]")
	}
	id, err := instanceID(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	timeout := fs.Duration("timeout", time.Hour, "how long to wait for the setup")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	return waitSetup(ctx, c, id, *timeout, out)
}
