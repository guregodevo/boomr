// hyperscalr rents GPU machines on vast.ai from the command line.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/guregodevo/hyperscalr/vast"
)

const usage = `hyperscalr rents GPU machines on vast.ai.

  hyperscalr offers [filters]                     the cheapest matching offers (no key needed)
  hyperscalr up --image IMG --disk GB [filters]   rent the cheapest matching offer
     [--count N]                              the N cheapest matches, one instance each
     [--with janis,memdoor]                   install them at start, return when they are ready
     [--onstart SCRIPT [--ready]]             run SCRIPT at start; --ready waits until it is done
  hyperscalr wait ID [--timeout 1h]               wait until an instance's setup is done
  hyperscalr deploy ID --with janis,memdoor       install or upgrade them on a running instance
  hyperscalr recipe janis,memdoor                 print the setup script --with runs
     --with's versions: --janis-ref main  --model Qwen3.8-27B-4bit  --model-rev main  --memdoor latest
  hyperscalr ls                                   your instances
  hyperscalr ssh ID [command]                     ssh into an instance
  hyperscalr logs ID [--tail N]                   the container's log
  hyperscalr stop ID [--keep]                     destroy one; --keep stops it and keeps its disk (billed)
  hyperscalr start ID                             start a kept one again
  hyperscalr rm ID                                destroy one and its disk
  hyperscalr credit                               the account's balance
  hyperscalr pool serve [--addr HOST:PORT]        the ring memdoor's requests go through, one box per run
  hyperscalr pool add ID... [--port 8093]         tunnel to each box's janis and add it (again: a no-op)
  hyperscalr pool ls                              the ring's boxes and runs
  hyperscalr key add [PUBKEY_FILE]                add an SSH public key to the account

filters: --gpu "H100 SXM,H100 PCIE"  --gpus 1  --min-gpu-ram GB  --max-price $/h
         --min-disk GB  --min-down Mb/s  --min-cuda 12.8  --unverified
         --exclude-geo CN,HK  --bid  --limit N

The API key is read from VAST_API_KEY, else ~/.config/vastai/vast_api_key.
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "hyperscalr:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(out, usage)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	c := vast.New()
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "offers":
		return offers(ctx, c, rest, out)
	case "up":
		return up(ctx, c, rest, out)
	case "ls":
		return ls(ctx, c, out)
	case "ssh":
		return sshInto(ctx, c, rest)
	case "logs":
		return logs(ctx, c, rest, out)
	case "wait":
		return waitCmd(ctx, c, rest, out)
	case "recipe":
		return recipeCmd(ctx, rest, out)
	case "deploy":
		return deploy(ctx, c, rest, out)
	case "pool":
		return poolCmd(ctx, c, rest, out)
	case "stop", "start", "rm":
		id, keep, err := lifecycleArgs(cmd, rest)
		if err != nil {
			return err
		}
		do, said := c.Destroy, "destroyed"
		switch {
		case cmd == "stop" && keep:
			do, said = c.Stop, fmt.Sprintf("stopped; its disk is kept and billed (hyperscalr start %d, hyperscalr rm %d)", id, id)
		case cmd == "stop":
			// A stopped box bills its disk, and its host may have rented the
			// GPU out by the time it is started again (vast.ai then queues
			// the start for as long as that lasts): a stop destroys unless
			// --keep says otherwise.
			said = "destroyed (hyperscalr stop --keep keeps its disk)"
		case cmd == "start":
			do, said = c.Start, "starting"
		}
		if err := do(ctx, id); err != nil {
			if cmd == "start" && errors.Is(err, vast.ErrUnavailable) {
				return fmt.Errorf("instance %d did not start: %v\nvast.ai queued the start: it runs, and bills, when the GPUs free up; `hyperscalr stop --keep %d` cancels it, `hyperscalr stop %d` destroys the instance", id, err, id, id)
			}
			return err
		}
		fmt.Fprintf(out, "instance %d %s\n", id, said)
		return nil
	case "credit":
		v, err := c.Credit(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "$%.2f\n", v)
		return nil
	case "key":
		return key(ctx, c, rest, out)
	}
	return fmt.Errorf("unknown command %q (hyperscalr help)", cmd)
}

type filters struct {
	gpu, excludeGeo         string
	gpus, limit             int
	minRAM, maxPrice        float64
	minDisk, minDown, minCU float64
	unverified, bid         bool
}

func (f *filters) register(fs *flag.FlagSet) {
	fs.StringVar(&f.gpu, "gpu", "", "GPU names, comma separated, as vast.ai writes them (\"H100 SXM\")")
	fs.IntVar(&f.gpus, "gpus", 1, "GPUs per machine")
	fs.Float64Var(&f.minRAM, "min-gpu-ram", 0, "least memory per GPU, GB")
	fs.Float64Var(&f.maxPrice, "max-price", 0, "most $/h for the whole machine")
	fs.Float64Var(&f.minDisk, "min-disk", 0, "least free disk on the host, GB (default 60)")
	fs.Float64Var(&f.minDown, "min-down", 0, "least download speed, Mb/s (default 500)")
	fs.Float64Var(&f.minCU, "min-cuda", 0, "least CUDA the host driver runs (default 12.8)")
	fs.StringVar(&f.excludeGeo, "exclude-geo", "CN,HK", "country codes to avoid, comma separated (\"\" for none)")
	fs.BoolVar(&f.unverified, "unverified", false, "take hosts vast.ai does not verify")
	fs.BoolVar(&f.bid, "bid", false, "the interruptible tier")
	fs.IntVar(&f.limit, "limit", 10, "offers to show")
}

func split(s string) []string {
	out := []string{}
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func (f filters) selectors() vast.Selectors {
	s := vast.Selectors{GPUNames: split(f.gpu), NumGPUs: f.gpus, MinGPURAMGB: f.minRAM, MaxPrice: f.maxPrice,
		MinDiskGB: f.minDisk, MinDownMbps: f.minDown, MinCUDA: f.minCU, ExcludeGeos: split(f.excludeGeo),
		AllowUnverified: f.unverified, Limit: f.limit}
	if f.bid {
		s.RentalType = vast.Bid
	}
	return s
}

func offers(ctx context.Context, c *vast.Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("offers", flag.ContinueOnError)
	var f filters
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	os, err := c.Offers(ctx, f.selectors())
	if err != nil {
		return err
	}
	if len(os) == 0 {
		return errors.New("no offer matches")
	}
	printOffers(out, os, f.bid)
	return nil
}

func printOffers(out io.Writer, os []vast.Offer, bid bool) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	price := "$/H"
	if bid {
		price = "MIN BID $/H"
	}
	fmt.Fprintf(w, "OFFER\tGPU\tGB\tGB/S\t%s\tHOST\tRELIABILITY\tDOWN MB/S\tCUDA\tWHERE\n", price)
	for _, o := range os {
		p := o.DPHTotal
		if bid {
			p = o.MinBid
		}
		fmt.Fprintf(w, "%d\t%d× %s\t%.0f\t%.0f\t%.2f\t%s\t%.3f\t%.0f\t%.1f\t%s\n", o.ID, o.NumGPUs, o.GPUName,
			o.GPURAMGB(), o.GPUMemBW, p, o.Verification, o.Reliability, o.InetDown, o.CUDAMax, o.Geolocation)
	}
	w.Flush()
}

func up(ctx context.Context, c *vast.Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	var f filters
	f.register(fs)
	var spec vast.Spec
	var offerID int64
	var onstart, with string
	var want pins
	var wait, ready bool
	var readyTimeout time.Duration
	var count int
	fs.StringVar(&spec.Image, "image", "", "the Docker image, pinned to a tag (required)")
	fs.Float64Var(&spec.DiskGB, "disk", 0, "the instance's disk, GB (required)")
	fs.StringVar(&spec.Label, "label", "", "a name for the instance (default: a minted hyperscalr-… label)")
	fs.StringVar(&onstart, "onstart", "", "a script file run when the instance starts")
	fs.Float64Var(&spec.BidPrice, "bid-price", 0, "with --bid, the $/h to bid (default: the offer's minimum)")
	fs.Int64Var(&offerID, "offer", 0, "rent this offer instead of the cheapest match")
	fs.IntVar(&count, "count", 1, "rent this many of the cheapest matching offers, one instance each")
	fs.BoolVar(&wait, "wait", true, "wait until the instance runs and print how to ssh in")
	fs.StringVar(&with, "with", "", "install these at start: "+strings.Join(recipes, ",")+" (implies --ready)")
	want.register(fs)
	fs.BoolVar(&ready, "ready", false, "then wait until the setup writes "+readyFile)
	fs.DurationVar(&readyTimeout, "ready-timeout", time.Hour, "how long --ready waits for the setup")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if onstart != "" && with != "" {
		return errors.New("--onstart or --with, not both")
	}
	if count < 1 {
		return errors.New("--count must be at least 1")
	}
	if count > 1 && (offerID != 0 || f.bid || spec.Label != "") {
		return errors.New("--count takes the cheapest offers: no --offer, --bid or --label")
	}
	if onstart != "" {
		b, err := os.ReadFile(onstart)
		if err != nil {
			return err
		}
		spec.OnStart = string(b)
	}
	if with != "" {
		s, err := pinnedScript(ctx, with, want, out)
		if err != nil {
			return err
		}
		spec.OnStart, ready = s, true
		if spec.Image == "" {
			spec.Image = recipeImage
		}
		if spec.DiskGB == 0 {
			spec.DiskGB = recipeDiskGB
		}
	}
	if err := spec.Validate(); err != nil {
		return err
	}
	offerIDs := []int64{offerID}
	if offerID == 0 {
		sel := f.selectors()
		if sel.MinDiskGB < spec.DiskGB {
			sel.MinDiskGB = spec.DiskGB
		}
		sel.Limit = count
		os, err := c.Offers(ctx, sel)
		if err != nil {
			return err
		}
		if len(os) < count {
			return fmt.Errorf("%d offers match, %d asked", len(os), count)
		}
		printOffers(out, os, f.bid)
		offerIDs = offerIDs[:0]
		for _, o := range os[:count] {
			offerIDs = append(offerIDs, o.ID)
		}
		if f.bid && spec.BidPrice == 0 {
			spec.BidPrice = os[0].MinBid
		}
	} else if f.bid && spec.BidPrice == 0 {
		// Without a price the rental is on-demand: --bid --offer rented an A40
		// at $0.71/h, not its $0.20 minimum bid (2026-11-24).
		o, err := c.Offer(ctx, offerID, vast.Bid)
		if err != nil {
			return err
		}
		spec.BidPrice = o.MinBid
	}
	if f.bid && spec.BidPrice <= 0 {
		return fmt.Errorf("offer %d has no bid price: pass --bid-price", offerID)
	}
	var ids []int64
	for _, oid := range offerIDs {
		id, label, err := c.Create(ctx, oid, spec)
		if err != nil {
			return fmt.Errorf("rented %v so far, then: %w", ids, err)
		}
		fmt.Fprintf(out, "instance %d (%s) rented from offer %d\n", id, label, oid)
		ids = append(ids, id)
	}
	if !wait {
		return nil
	}
	for _, id := range ids {
		if err := waitInstance(ctx, c, id, ready, readyTimeout, out); err != nil {
			return err
		}
	}
	return nil
}

func waitInstance(ctx context.Context, c *vast.Client, id int64, ready bool, readyTimeout time.Duration, out io.Writer) error {
	for {
		in, err := c.Instance(ctx, id)
		if err != nil {
			return err
		}
		if host, port, ok := in.SSH(); in.Running() && ok {
			fmt.Fprintf(out, "running: ssh -p %s root@%s   (hyperscalr ssh %d)\n", port, host, id)
			if !ready {
				return nil
			}
			return waitSetup(ctx, c, id, readyTimeout, out)
		}
		fmt.Fprintf(out, "  %s %s\n", in.ActualStatus, strings.TrimSpace(in.StatusMsg))
		select {
		case <-ctx.Done():
			return fmt.Errorf("instance %d not running yet: %w (hyperscalr ls)", id, ctx.Err())
		case <-time.After(10 * time.Second):
		}
	}
}

func ls(ctx context.Context, c *vast.Client, out io.Writer) error {
	is, err := c.Instances(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tLABEL\tSTATE\tGPU\t$/H\tUP\tSSH\tWHERE")
	for _, i := range is {
		to := "-"
		if host, port, ok := i.SSH(); ok {
			to = fmt.Sprintf("root@%s -p %s", host, port)
		}
		up := "-"
		if i.StartDate > 0 && i.Running() {
			up = time.Since(time.Unix(int64(i.StartDate), 0)).Round(time.Minute).String()
		}
		fmt.Fprintf(w, "%d\t%s\t%s\t%d× %s\t%.2f\t%s\t%s\t%s\n", i.ID, i.Label, i.ActualStatus, i.NumGPUs, i.GPUName, i.DPHTotal, up, to, i.Geolocation)
	}
	return w.Flush()
}

func sshInto(ctx context.Context, c *vast.Client, args []string) error {
	id, err := instanceID(args)
	if err != nil {
		return err
	}
	in, err := c.Instance(ctx, id)
	if err != nil {
		return err
	}
	host, port, ok := in.SSH()
	if !ok {
		return fmt.Errorf("instance %d has no ssh address yet (state %s)", id, in.ActualStatus)
	}
	cmd := exec.Command("ssh", append([]string{"-p", port, "-o", "StrictHostKeyChecking=accept-new", "root@" + host}, args[1:]...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func logs(ctx context.Context, c *vast.Client, args []string, out io.Writer) error {
	id, err := instanceID(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	tail := fs.Int("tail", 200, "lines")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	s, err := c.Logs(ctx, id, *tail)
	if err != nil {
		return err
	}
	fmt.Fprint(out, s)
	return nil
}

func key(ctx context.Context, c *vast.Client, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "add" {
		return errors.New("usage: hyperscalr key add [PUBKEY_FILE]")
	}
	path := ""
	if len(args) > 1 {
		path = args[1]
	} else {
		home, _ := os.UserHomeDir()
		for _, n := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
			if p := filepath.Join(home, ".ssh", n); fileExists(p) {
				path = p
				break
			}
		}
		if path == "" {
			return errors.New("no ~/.ssh/id_*.pub; name the public key file")
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := c.AddSSHKey(ctx, string(b)); err != nil {
		return err
	}
	fmt.Fprintf(out, "added %s to the account\n", path)
	return nil
}

// lifecycleArgs reads stop, start and rm's arguments: the id, and for a
// stop whether --keep (before or after it) keeps the instance's disk.
func lifecycleArgs(cmd string, args []string) (id int64, keep bool, err error) {
	keep = slices.Contains(args, "--keep")
	if keep && cmd != "stop" {
		return 0, false, fmt.Errorf("--keep is for hyperscalr stop")
	}
	id, err = instanceID(slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--keep" }))
	return id, keep, err
}

func instanceID(args []string) (int64, error) {
	if len(args) == 0 {
		return 0, errors.New("name the instance id (hyperscalr ls)")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("instance id %q is not a number", args[0])
	}
	return id, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
