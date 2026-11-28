package main

import (
	"embed"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

//go:embed recipes/*.sh
var recipeFiles embed.FS

// recipes are what `hyperscalr up --with` installs, in the order they run.
var recipes = []string{"janis", "memdoor"}

// recipeImage and recipeDisk are up's defaults with --with: CUDA for
// janis's kernels, room for a 27B at 4 bits and its build.
const (
	recipeImage  = "nvidia/cuda:12.9.1-devel-ubuntu22.04"
	recipeDiskGB = 60
)

// recipeValue is what a pinned setting may hold: a ref, a commit, a model
// name or a tag.
var recipeValue = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// setupScript is the script that installs with, in recipes' order, at the
// versions p pins (resolvePins).
func setupScript(with []string, p pins) (string, error) {
	for _, w := range with {
		if !slices.Contains(recipes, w) {
			return "", fmt.Errorf("no recipe %q: hyperscalr installs %s", w, strings.Join(recipes, ", "))
		}
	}
	vars := [][2]string{}
	if slices.Contains(with, "janis") {
		vars = append(vars, [2]string{"JANIS_REF", p.JanisRef}, [2]string{"JANIS_COMMIT", p.JanisCommit}, [2]string{"MODEL", p.Model}, [2]string{"MODEL_REV", p.ModelRev})
	}
	if slices.Contains(with, "memdoor") {
		vars = append(vars, [2]string{"MEMDOOR_RELEASE", p.Memdoor})
	}
	var b strings.Builder
	b.WriteString(part("head"))
	for _, v := range vars {
		if !recipeValue.MatchString(v[1]) {
			return "", fmt.Errorf("%s %q: letters, digits and . _ / - only", v[0], v[1])
		}
		fmt.Fprintf(&b, "export %s=%s\n", v[0], v[1])
	}
	for _, r := range recipes {
		if slices.Contains(with, r) {
			b.WriteString("\n" + part(r))
		}
	}
	b.WriteString("\n" + part("tail"))
	return b.String(), nil
}

func part(name string) string {
	b, err := recipeFiles.ReadFile("recipes/" + name + ".sh")
	if err != nil {
		panic(err) // embedded: a missing part is a build mistake
	}
	return string(b)
}
