package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// The fake `go` and the fake staticcheck of the Makefile tests are executables
// compiled here, named with the platform's suffix (binExeSuffix). They used to be
// `#!/bin/sh` files with no extension, which are found only by a command that
// goes through `sh`.
//
// The CI log of run 42577703029, the first one that ran `make lint` for real on
// Windows, shows the split: `go version` (inside `$$(...)`, so through sh) found
// the script and printed "go version fake ...", while the three `go env` of the
// Makefile's parse time and `go vet ./...` (plain commands) reached the REAL go,
// which read the `toolchain go1.99.9` of the sandbox go.mod and tried to download
// it. The fake was called by one command out of five, and nothing in the test
// could say so. A program with the executable suffix is what a process search
// looks for (whether that holds on Windows is for the CI to show: it was not run
// there when this was written), and it writes each call it receives to
// FAKE_TOOL_LOG, so a test can tell which commands the fake answered and not only
// what one of them printed.
//
// What the two fakes print is what the Makefile tests assert on:
//
//	go version        "go version fake <GOTOOLCHAIN>"
//	go vet            "VET_SAW [<GOTOOLCHAIN>]"
//	staticcheck -ver  "staticcheck 2026.2.1 (<FAKE_STATICCHECK_PIN>)"
//	staticcheck       "STATICCHECK_SAW [<GOTOOLCHAIN>]"
//
//	go env <NAME>     the value of FAKE_GOENV_<NAME> when the test set it (F-174: a path
//	                  with a `#` in it is what the Makefile must not read as syntax)
//
// and any other `go` command, or a `go env` the test gave no answer for, is
// answered with silence and exit 0 (the Makefile asks `go env` when a recipe needs
// its answer, F-174).
const fakeToolSource = `package main

import (
	"fmt"
	"os"
	"strings"
)

// tool is set by the build: "go" or "staticcheck".
var tool string

func main() {
	if tool != "go" && tool != "staticcheck" {
		fmt.Fprintln(os.Stderr, "fake tool built without a name:", tool)
		os.Exit(2)
	}
	toolchain := os.Getenv("GOTOOLCHAIN")
	args := os.Args[1:]
	if err := logCall(toolchain, args); err != nil {
		fmt.Fprintln(os.Stderr, "fake "+tool+": cannot write the call log:", err)
		os.Exit(2)
	}
	first := ""
	if len(args) > 0 {
		first = args[0]
	}
	switch {
	case tool == "go" && first == "version":
		fmt.Printf("go version fake %s\n", toolchain)
	case tool == "go" && first == "vet":
		fmt.Printf("VET_SAW [%s]\n", toolchain)
	case tool == "go" && first == "env" && len(args) == 2:
		if answer := os.Getenv("FAKE_GOENV_" + args[1]); answer != "" {
			fmt.Println(answer)
		}
	case tool == "staticcheck" && first == "--version":
		fmt.Printf("staticcheck 2026.2.1 (%s)\n", os.Getenv("FAKE_STATICCHECK_PIN"))
	case tool == "staticcheck":
		fmt.Printf("STATICCHECK_SAW [%s]\n", toolchain)
	}
}

// logCall appends "<tool> <args> [<GOTOOLCHAIN>]" to FAKE_TOOL_LOG, if set.
func logCall(toolchain string, args []string) error {
	path := os.Getenv("FAKE_TOOL_LOG")
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(f, "%s %s [%s]\n", tool, strings.Join(args, " "), toolchain)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}
`

var (
	fakeToolsDir  string
	fakeToolsErr  error
	fakeToolsOnce sync.Once
)

// fakeTools compiles the two fakes once per package run and returns the
// directory that holds them. Like the harness binary of buildBinary, the
// directory is left in the system temp dir: nothing owns it for the whole run.
func fakeTools(t *testing.T) string {
	t.Helper()
	fakeToolsOnce.Do(func() { fakeToolsDir, fakeToolsErr = buildFakeTools() })
	require.NoError(t, fakeToolsErr)
	return fakeToolsDir
}

func buildFakeTools() (string, error) {
	root, err := os.MkdirTemp("", "cab-int-faketools-*")
	if err != nil {
		return "", err
	}
	src, bin := filepath.Join(root, "src"), filepath.Join(root, "bin")
	for _, dir := range []string{src, bin} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return "", err
		}
	}
	for name, body := range map[string]string{"main.go": fakeToolSource, "go.mod": "module faketool\n\ngo 1.21\n"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o600); err != nil {
			return "", err
		}
	}

	// A module of its own and GOTOOLCHAIN=local: building the fakes must not depend
	// on the repository's go.mod, switch toolchain, or reach the network.
	env := os.Environ()
	for _, key := range []string{"GOTOOLCHAIN", "GOFLAGS", "GOWORK", "CGO_ENABLED"} {
		env = envWithout(env, key)
	}
	env = append(env, "GOTOOLCHAIN=local", "GOFLAGS=", "GOWORK=off", "CGO_ENABLED=0")

	for _, tool := range []string{"go", "staticcheck"} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(bin, tool+binExeSuffix), "-ldflags", "-X main.tool="+tool, ".")
		cmd.Dir = src
		cmd.Env = env
		if out, berr := cmd.CombinedOutput(); berr != nil {
			return "", fmt.Errorf("building the fake %s: %w\n%s", tool, berr, out)
		}
	}
	return bin, nil
}
