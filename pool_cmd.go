package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/guregodevo/boostr/pool"
)

// poolCmd is `boostr pool serve|ls|add|drain|remove`: the ring memdoor asks
// for a box per step, and its scale loop. The ring is in memory for now.
func poolCmd(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("boostr pool serve|ls|add|drain|remove")
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
	}
	return fmt.Errorf("boostr pool %s: not yet", args[0])
}
