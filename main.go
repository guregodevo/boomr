// boostr rents GPU machines on vast.ai from the command line.
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
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/guregodevo/boostr/vast"
)

const usage = `boostr rents GPU machines on vast.ai.

  boostr offers [filters]                     the cheapest matching offers (no key needed)
  boostr up --image IMG --disk GB [filters]   rent the cheapest matching offer
  boostr ls                                   your instances
  boostr ssh ID [command]                     ssh into an instance
  boostr logs ID [--tail N]                   the container's log
  boostr stop ID | start ID                   stop (the disk is still billed) or start one
  boostr rm ID                                destroy one and its disk
  boostr credit                               the account's balance
  boostr key add [PUBKEY_FILE]                add an SSH public key to the account

filters: --gpu "H100 SXM,H100 PCIE"  --gpus 1  --min-gpu-ram GB  --max-price $/h
         --min-disk GB  --min-down Mb/s  --min-cuda 12.8  --unverified
         --exclude-geo CN,HK  --bid  --limit N

The API key is read from VAST_API_KEY, else ~/.config/vastai/vast_api_key.
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "boostr:", err)
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
	case "stop", "start", "rm":
		id, err := instanceID(rest)
		if err != nil {
			return err
		}
		do, said := c.Stop, "stopped"
		switch cmd {
		case "start":
			do, said = c.Start, "starting"
		case "rm":
			do, said = c.Destroy, "destroyed"
		}
		if err := do(ctx, id); err != nil {
			if cmd == "start" && errors.Is(err, vast.ErrUnavailable) {
				return fmt.Errorf("instance %d did not start: %v\nvast.ai queued the start: it runs, and bills, when the GPUs free up; `boostr stop %d` cancels it", id, err, id)
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
	return fmt.Errorf("unknown command %q (boostr help)", cmd)
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
	var onstart string
	var wait bool
	fs.StringVar(&spec.Image, "image", "", "the Docker image, pinned to a tag (required)")
	fs.Float64Var(&spec.DiskGB, "disk", 0, "the instance's disk, GB (required)")
	fs.StringVar(&spec.Label, "label", "", "a name for the instance (default: a minted boostr-… label)")
	fs.StringVar(&onstart, "onstart", "", "a script file run when the instance starts")
	fs.Float64Var(&spec.BidPrice, "bid-price", 0, "with --bid, the $/h to bid (default: the offer's minimum)")
	fs.Int64Var(&offerID, "offer", 0, "rent this offer instead of the cheapest match")
	fs.BoolVar(&wait, "wait", true, "wait until the instance runs and print how to ssh in")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if onstart != "" {
		b, err := os.ReadFile(onstart)
		if err != nil {
			return err
		}
		spec.OnStart = string(b)
	}
	if err := spec.Validate(); err != nil {
		return err
	}
	if offerID == 0 {
		sel := f.selectors()
		if sel.MinDiskGB < spec.DiskGB {
			sel.MinDiskGB = spec.DiskGB
		}
		sel.Limit = 1
		os, err := c.Offers(ctx, sel)
		if err != nil {
			return err
		}
		if len(os) == 0 {
			return errors.New("no offer matches")
		}
		printOffers(out, os, f.bid)
		offerID = os[0].ID
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
	id, label, err := c.Create(ctx, offerID, spec)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "instance %d (%s) rented from offer %d\n", id, label, offerID)
	if !wait {
		return nil
	}
	for {
		in, err := c.Instance(ctx, id)
		if err != nil {
			return err
		}
		if host, port, ok := in.SSH(); in.Running() && ok {
			fmt.Fprintf(out, "running: ssh -p %s root@%s   (boostr ssh %d)\n", port, host, id)
			return nil
		}
		fmt.Fprintf(out, "  %s %s\n", in.ActualStatus, strings.TrimSpace(in.StatusMsg))
		select {
		case <-ctx.Done():
			return fmt.Errorf("instance %d not running yet: %w (boostr ls)", id, ctx.Err())
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
		return errors.New("usage: boostr key add [PUBKEY_FILE]")
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

func instanceID(args []string) (int64, error) {
	if len(args) == 0 {
		return 0, errors.New("name the instance id (boostr ls)")
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
