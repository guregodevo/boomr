package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/guregodevo/hyperscalr/pool"
	"github.com/guregodevo/hyperscalr/vast"
)

// poolCmd is `hyperscalr pool serve|ls|add|drain|remove`: the ring memdoor asks
// for a box per run, and its scale loop. The ring is in memory for now.
func poolCmd(ctx context.Context, c *vast.Client, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("hyperscalr pool serve|ls|add|drain|remove")
	}
	switch args[0] {
	case "serve":
		fs := flag.NewFlagSet("pool serve", flag.ContinueOnError)
		addr := fs.String("addr", "127.0.0.1:18900", "address to serve the ring on")
		min := fs.Int("min", 1, "fewest boxes")
		max := fs.Int("max", 4, "most boxes")
		idle := fs.Duration("idle", 15*time.Minute, "drain a box idle this long")
		every := fs.Duration("every", 30*time.Second, "how often the scale policy runs")
		act := fs.Bool("act", false, "rent and destroy boxes; without it the loop only logs")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *act {
			return fmt.Errorf("pool serve --act: renting and destroying are not wired yet; run without it")
		}
		srv := pool.NewServer(pool.New(0, 0))
		go pool.Loop(ctx, srv, pool.Policy{Min: *min, Max: *max, IdleFor: *idle}, nil, *every, *act,
			func(f string, a ...any) { log.Printf(f, a...) })
		fmt.Fprintf(out, "pool serving on http://%s (scale every %s, act %v)\n", *addr, *every, *act)
		return (&http.Server{Addr: *addr, Handler: srv.Handler()}).ListenAndServe()
	case "add":
		fs := flag.NewFlagSet("pool add", flag.ContinueOnError)
		poolURL := fs.String("pool", defaultPool, "the pool's address")
		remote := fs.Int("port", 8093, "janis's port on the box")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			return errors.New("hyperscalr pool add ID... [--pool URL] [--port 8093]")
		}
		for _, a := range fs.Args() {
			id, err := strconv.ParseInt(a, 10, 64)
			if err != nil {
				return fmt.Errorf("instance id %q: %w", a, err)
			}
			tunnel := func() (string, error) { return sshTunnel(ctx, c, id, *remote) }
			if err := addBox(ctx, *poolURL, a, tunnel, out); err != nil {
				return err
			}
		}
		return nil
	case "ls":
		fs := flag.NewFlagSet("pool ls", flag.ContinueOnError)
		poolURL := fs.String("pool", defaultPool, "the pool's address")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return poolLs(ctx, *poolURL, out)
	}
	return fmt.Errorf("hyperscalr pool %s: not yet", args[0])
}

const defaultPool = "http://127.0.0.1:18900"

// healthWithin is how long pool add waits for a new box's janis to answer.
var healthWithin = 30 * time.Second

// addBox opens the way to a box's janis, checks janis answers, and adds the
// box to the pool's ring under its instance id.
// A box already in the ring whose janis answers is left as it is: adding it
// again is a no-op. One whose endpoint no longer answers gets a new tunnel.
func addBox(ctx context.Context, poolURL, id string, tunnel func() (string, error), out io.Writer) error {
	st, err := fetchRing(ctx, poolURL)
	if err != nil {
		return err
	}
	for _, m := range st.Members {
		if m.ID == id && waitHealth(ctx, m.Endpoint, 0) == nil {
			fmt.Fprintf(out, "instance %s already in the pool: %s\n", id, m.Endpoint)
			return nil
		}
	}
	endpoint, err := tunnel()
	if err != nil {
		return fmt.Errorf("instance %s: %w", id, err)
	}
	if err := waitHealth(ctx, endpoint, healthWithin); err != nil {
		return fmt.Errorf("instance %s: janis does not answer at %s: %w (is janis serve running on the box?)", id, endpoint, err)
	}
	body, _ := json.Marshal(pool.Member{ID: id, Endpoint: endpoint, ProviderID: "box-" + id})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, poolURL+"/add", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("the pool at %s: %w (hyperscalr pool serve)", poolURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("the pool refused instance %s: %s %s", id, resp.Status, bytes.TrimSpace(b))
	}
	fmt.Fprintf(out, "instance %s added: %s\n", id, endpoint)
	return nil
}

func waitHealth(ctx context.Context, endpoint string, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/health", nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("/health: %s", resp.Status)
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// sshTunnel forwards a free local port to the box's janis port over the
// instance's ssh address. ssh -f stays in the background once the forward
// is up; the tunnel outlives this command.
func sshTunnel(ctx context.Context, c *vast.Client, id int64, remote int) (string, error) {
	in, err := c.Instance(ctx, id)
	if err != nil {
		return "", err
	}
	host, port, ok := in.SSH()
	if !ok || !in.Running() {
		return "", fmt.Errorf("not running with an ssh address (state %s)", in.ActualStatus)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	local := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.CommandContext(ctx, "ssh", "-f", "-N",
		"-o", "ExitOnForwardFailure=yes", "-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=30", "-o", "LogLevel=ERROR",
		"-L", fmt.Sprintf("%d:127.0.0.1:%d", local, remote), "-p", port, "root@"+host)
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ssh tunnel: %w: %s", err, bytes.TrimSpace(b))
	}
	return fmt.Sprintf("http://127.0.0.1:%d", local), nil
}

type ringState struct {
	Members []pool.Member     `json:"members"`
	Runs    []json.RawMessage `json:"runs"`
}

func fetchRing(ctx context.Context, poolURL string) (ringState, error) {
	var st ringState
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, poolURL+"/ring", nil)
	if err != nil {
		return st, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return st, fmt.Errorf("the pool at %s: %w (hyperscalr pool serve)", poolURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return st, fmt.Errorf("the pool at %s: /ring %s", poolURL, resp.Status)
	}
	return st, json.NewDecoder(resp.Body).Decode(&st)
}

func poolLs(ctx context.Context, poolURL string, out io.Writer) error {
	st, err := fetchRing(ctx, poolURL)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tENDPOINT\tACTIVE\tDRAINING")
	for _, m := range st.Members {
		fmt.Fprintf(w, "%s\t%s\t%d\t%v\n", m.ID, m.Endpoint, m.Active, m.Draining)
	}
	w.Flush()
	fmt.Fprintf(out, "%d boxes, %d runs placed\n", len(st.Members), len(st.Runs))
	return nil
}
