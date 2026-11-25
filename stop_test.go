package main

import (
	"strings"
	"testing"
)

// The help says what a stop does: it destroys, --keep keeps the disk.
func TestTheHelpSaysAStopDestroys(t *testing.T) {
	var out strings.Builder
	if err := run([]string{"help"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "boostr stop ID [--keep]") || !strings.Contains(out.String(), "destroy one; --keep stops it") {
		t.Errorf("help:\n%s", out.String())
	}
}

// A STOP DESTROYS UNLESS --KEEP, before or after the id.
func TestAStopKeepsTheDiskOnlyWithKeep(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		args []string
		id   int64
		keep bool
		err  bool
	}{
		{"stop", []string{"42"}, 42, false, false},
		{"stop", []string{"42", "--keep"}, 42, true, false},
		{"stop", []string{"--keep", "42"}, 42, true, false},
		{"rm", []string{"42"}, 42, false, false},
		{"start", []string{"42", "--keep"}, 0, false, true},
		{"stop", []string{"--keep"}, 0, true, true},
	} {
		id, keep, err := lifecycleArgs(c.cmd, c.args)
		if id != c.id || keep != c.keep || (err != nil) != c.err {
			t.Errorf("%s %v: id %d keep %v err %v", c.cmd, c.args, id, keep, err)
		}
	}
}
