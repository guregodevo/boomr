package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var pinned = pins{JanisRef: "main", JanisCommit: "f72dc37da3879c7860784ac8abf2b6de1c794508", Model: "Qwen3.8-27B-4bit", ModelRev: "b935172e4bb5dcd0543774a34748fb92422e3027", Memdoor: "v1.840.0"}

// --WITH WRITES ONE SETUP SCRIPT FOR WHAT IT NAMES, at pinned versions.
//
// "memdoor,janis" installs janis first, with its commit, the model's
// revision and memdoor's release exported before; the script ends by
// writing the manifest and the ready file, and bash reads it.
func TestWithWritesOneScriptAtPinnedVersions(t *testing.T) {
	s, err := setupScript([]string{"memdoor", "janis"}, pinned)
	if err != nil {
		t.Fatal(err)
	}
	j, m := strings.Index(s, `echo "== janis`), strings.Index(s, `echo "== memdoor`)
	if j < 0 || m < 0 || j > m {
		t.Errorf("janis at %d, memdoor at %d: want both, janis first", j, m)
	}
	for _, want := range []string{
		"export JANIS_COMMIT=f72dc37da3879c7860784ac8abf2b6de1c794508\n",
		"export MODEL_REV=b935172e4bb5dcd0543774a34748fb92422e3027\n",
		"export MEMDOOR_RELEASE=v1.840.0\n",
		"MEMDOOR_GITHUB_RELEASE=\"$MEMDOOR_RELEASE\"",
		"resolve/$MODEL_REV/",
		"mv /root/boostr-manifest.new /root/boostr-manifest\ntouch /root/boostr-ready\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in the script", want)
		}
	}
	if strings.Contains(s, "janis pull") {
		t.Error("janis pull fetches the newest revision, not the pinned one")
	}
	f := filepath.Join(t.TempDir(), "setup.sh")
	if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", f).CombinedOutput(); err != nil {
		t.Errorf("bash -n: %v\n%s", err, out)
	}
}

// memdoor alone installs no janis and no Go, and exports no janis setting.
func TestMemdoorAloneInstallsOnlyMemdoor(t *testing.T) {
	s, err := setupScript([]string{"memdoor"}, pins{Memdoor: "v1.840.0"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s, "== janis") || strings.Contains(s, "== go") || strings.Contains(s, "JANIS_COMMIT") {
		t.Error("memdoor alone builds janis")
	}
}

// An unknown recipe or a setting that is not one word is refused.
func TestAnUnknownRecipeOrABadSettingIsRefused(t *testing.T) {
	if _, err := setupScript([]string{"vllm"}, pinned); err == nil || !strings.Contains(err.Error(), "janis, memdoor") {
		t.Errorf("unknown recipe: %v", err)
	}
	for _, v := range []string{"main; rm -rf /", "a b", "$(id)", "x'y", ""} {
		p := pinned
		p.JanisCommit = v
		if _, err := setupScript([]string{"janis"}, p); err == nil {
			t.Errorf("JANIS_COMMIT %q accepted", v)
		}
	}
}

// THE MOVING NAMES ARE RESOLVED TO EXACT VERSIONS when the box is asked for:
// janis's branch to its commit, the model's main to its revision, memdoor's
// latest to its tag; and the flags that ask for them again say so.
func TestMovingNamesAreResolvedToExactVersions(t *testing.T) {
	asked := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked[r.URL.Path] = true
		switch r.URL.Path {
		case "/repos/guregodevo/janis/commits/main":
			w.Write([]byte(`{"sha":"f72dc37da3879c7860784ac8abf2b6de1c794508"}`))
		case "/api/models/mlx-community/Qwen3.8-27B-4bit/revision/main":
			w.Write([]byte(`{"sha":"b935172e4bb5dcd0543774a34748fb92422e3027"}`))
		case "/repos/guregodevo/memdoor/releases/latest":
			w.Write([]byte(`{"tag_name":"v1.840.0"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	githubAPI, hubAPI = srv.URL, srv.URL
	defer func() { githubAPI, hubAPI = "https://api.github.com", "https://huggingface.co" }()

	p, err := resolvePins(context.Background(), []string{"janis", "memdoor"}, pins{})
	if err != nil {
		t.Fatal(err)
	}
	if p != pinned {
		t.Errorf("pins %+v, want %+v", p, pinned)
	}
	want := "--with janis,memdoor --janis-ref f72dc37da3879c7860784ac8abf2b6de1c794508 --model Qwen3.8-27B-4bit --model-rev b935172e4bb5dcd0543774a34748fb92422e3027 --memdoor v1.840.0"
	if got := p.args("janis,memdoor"); got != want {
		t.Errorf("args %q\nwant %q", got, want)
	}

	if _, err := resolvePins(context.Background(), []string{"janis"}, pins{JanisRef: "no-such-branch"}); err == nil {
		t.Error("an unknown janis ref resolved")
	}
	clear(asked)
	if _, err := resolvePins(context.Background(), []string{"memdoor"}, pins{}); err != nil || len(asked) != 1 {
		t.Errorf("memdoor alone: %v, asked %v", err, asked)
	}
}
