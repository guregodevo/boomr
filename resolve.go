package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// The services versions are resolved at: a test points them at a local
// server.
var (
	githubAPI = "https://api.github.com"
	hubAPI    = "https://huggingface.co"
)

// pins are the exact versions a setup installs: what makes it repeatable.
// A box given the same pins again changes nothing; other pins upgrade (or
// roll back) what differs.
type pins struct {
	JanisRef, JanisCommit string // what was asked, and the commit it named
	Model, ModelRev       string // an mlx-community model, its revision
	Memdoor               string // a memdoor release tag
}

// resolvePins turns the moving names in want ("main", the model's "main",
// "latest") into the versions they name now, for the recipes in with.
func resolvePins(ctx context.Context, with []string, want pins) (pins, error) {
	p := want
	if p.JanisRef == "" {
		p.JanisRef = "main"
	}
	if p.Model == "" {
		p.Model = "Qwen3.8-27B-4bit"
	}
	if p.ModelRev == "" {
		p.ModelRev = "main"
	}
	if p.Memdoor == "" {
		p.Memdoor = "latest"
	}
	for _, w := range with {
		switch w {
		case "janis":
			var c struct{ SHA string }
			if err := getJSON(ctx, githubAPI+"/repos/guregodevo/janis/commits/"+url.PathEscape(p.JanisRef), &c); err != nil {
				return p, fmt.Errorf("janis %s: %w", p.JanisRef, err)
			}
			p.JanisCommit = c.SHA
			var m struct{ SHA string }
			if err := getJSON(ctx, hubAPI+"/api/models/mlx-community/"+url.PathEscape(p.Model)+"/revision/"+url.PathEscape(p.ModelRev), &m); err != nil {
				return p, fmt.Errorf("model %s at %s: %w", p.Model, p.ModelRev, err)
			}
			p.ModelRev = m.SHA
		case "memdoor":
			path := "/releases/tags/" + url.PathEscape(p.Memdoor)
			if p.Memdoor == "latest" {
				path = "/releases/latest"
			}
			var r struct {
				TagName string `json:"tag_name"`
			}
			if err := getJSON(ctx, githubAPI+"/repos/guregodevo/memdoor"+path, &r); err != nil {
				return p, fmt.Errorf("memdoor %s: %w", p.Memdoor, err)
			}
			p.Memdoor = r.TagName
		}
	}
	return p, nil
}

func getJSON(ctx context.Context, u string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// args are the flags that ask for these pins again: the same box.
func (p pins) args(with string) string {
	s := "--with " + with
	if p.JanisCommit != "" {
		s += fmt.Sprintf(" --janis-ref %s --model %s --model-rev %s", p.JanisCommit, p.Model, p.ModelRev)
	}
	if p.Memdoor != "" && p.Memdoor != "latest" {
		s += " --memdoor " + p.Memdoor
	}
	return s
}
