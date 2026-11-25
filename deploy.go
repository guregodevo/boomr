package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/guregodevo/boostr/vast"
)

func (p *pins) register(fs *flag.FlagSet) {
	fs.StringVar(&p.JanisRef, "janis-ref", "", "with janis: the branch, tag or commit built (default main)")
	fs.StringVar(&p.Model, "model", "", "with janis: the mlx-community model (default Qwen3.8-27B-4bit)")
	fs.StringVar(&p.ModelRev, "model-rev", "", "with janis: the model's revision (default main)")
	fs.StringVar(&p.Memdoor, "memdoor", "", "with memdoor: the release tag (default latest)")
}

// pinnedScript resolves want for with, says what will be installed and how
// to ask for the same again, and writes the setup script.
func pinnedScript(ctx context.Context, with string, want pins, out io.Writer) (string, error) {
	names := split(with)
	p, err := resolvePins(ctx, names, want)
	if err != nil {
		return "", err
	}
	s, err := setupScript(names, p)
	if err != nil {
		return "", err
	}
	if p.JanisCommit != "" {
		fmt.Fprintf(out, "janis   %s (%s)\nmodel   %s at %s\n", p.JanisCommit, p.JanisRef, p.Model, p.ModelRev)
	}
	if strings.Contains(with, "memdoor") {
		fmt.Fprintf(out, "memdoor %s\n", p.Memdoor)
	}
	fmt.Fprintf(out, "the same versions again: %s\n", p.args(with))
	return s, nil
}

// recipeCmd is `boostr recipe janis,memdoor [versions]`: the script --with
// would run now.
func recipeCmd(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: boostr recipe %s [--janis-ref R] [--model M] [--model-rev R] [--memdoor TAG]", strings.Join(recipes, ","))
	}
	fs := flag.NewFlagSet("recipe", flag.ContinueOnError)
	var want pins
	want.register(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	s, err := pinnedScript(ctx, args[0], want, io.Discard)
	if err != nil {
		return err
	}
	fmt.Fprint(out, s)
	return nil
}

// deploy is `boostr deploy ID --with janis,memdoor [versions]`: the setup
// run on an instance that is already up, as the script its next start
// runs too. What is at the asked version is kept, what differs upgraded.
func deploy(ctx context.Context, c *vast.Client, args []string, out io.Writer) error {
	id, err := instanceID(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	var want pins
	var with string
	fs.StringVar(&with, "with", "", "what to install or upgrade: "+strings.Join(recipes, ","))
	want.register(fs)
	timeout := fs.Duration("timeout", 0, "how long to wait for the setup (default 1h)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if with == "" {
		return errors.New("usage: boostr deploy ID --with " + strings.Join(recipes, ","))
	}
	s, err := pinnedScript(ctx, with, want, out)
	if err != nil {
		return err
	}
	in, err := c.Instance(ctx, id)
	if err != nil {
		return err
	}
	host, port, ok := in.SSH()
	if !in.Running() || !ok {
		return fmt.Errorf("instance %d is not running (state %s): boostr start %d first", id, in.ActualStatus, id)
	}
	// The markers go before the script starts, so the wait never reads the
	// last setup's ready file.
	run := "rm -f " + readyFile + " " + failedFile + " && cat > /root/boostr-setup.sh && BOOSTR_SETUP=1 setsid -f bash /root/boostr-setup.sh < /dev/null > /dev/null 2>&1"
	cmd := exec.CommandContext(ctx, "ssh", "-p", port, "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=15", "root@"+host, run)
	cmd.Stdin = strings.NewReader(s)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("deploy to %d: %v: %s", id, err, strings.TrimSpace(string(b)))
	}
	fmt.Fprintf(out, "deploying to %d\n", id)
	t := *timeout
	if t == 0 {
		t = defaultSetupTimeout
	}
	return waitSetup(ctx, c, id, t, out)
}
