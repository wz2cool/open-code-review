// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// childSpec is one `ocr review` child process: a human-readable label for
// progress prefixes and warnings, the full argument list, and the temp file
// its JSON report is written to.
type childSpec struct {
	Label   string
	Args    []string
	OutPath string
}

type childResult struct {
	Spec   childSpec
	Output []byte
	Err    error
}

// candidateNames lists the child binary names: the install scripts install
// `ocr`, while a development build next to ocr_ext is called
// `opencodereview` (the Makefile's BINARY_NAME). The .exe variants only
// exist on Windows but are harmless to probe elsewhere.
func candidateNames() []string {
	names := []string{"ocr", "opencodereview"}
	for _, n := range append([]string(nil), names...) {
		names = append(names, n+".exe")
	}
	return names
}

// findOCRBinary locates the child binary: a sibling of the running ocr_ext
// first (installed side by side, so versions pair naturally), then PATH.
func findOCRBinary() (string, error) {
	if self, err := os.Executable(); err == nil {
		dir := filepath.Dir(self)
		for _, name := range candidateNames() {
			p := filepath.Join(dir, name)
			if isExecutableFile(p) {
				return p, nil
			}
		}
	}
	for _, name := range candidateNames() {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no ocr binary found next to %s or on PATH; install ocr to use ocr_ext", os.Args[0])
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	// Windows synthesizes permission bits (files report no execute bits), so
	// a regular file with a binary name is accepted there.
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

// versionMismatch compares the child's reported version against ocr_ext's
// own ldflags version. Dev builds skip the check because they carry no
// meaningful version. An empty result means "no warning".
func versionMismatch(bin string) string {
	if Version == "" || Version == "dev" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return fmt.Sprintf("could not read the child binary's version: %v", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return ""
	}
	if fields[1] == Version {
		return ""
	}
	return fmt.Sprintf("child %s reports version %s but ocr_ext is %s; keep both binaries from the same build",
		filepath.Base(bin), fields[1], Version)
}

// runChildren runs every child spec concurrently and returns results in spec
// order. Cancelling the context kills the children. Each child's stderr is
// relayed to the parent's stderr line by line with a label prefix so
// interleaved progress stays attributable.
func runChildren(ctx context.Context, bin string, specs []childSpec) []childResult {
	results := make([]childResult, len(specs))
	var wg sync.WaitGroup
	for i := range specs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = runChild(ctx, bin, specs[i])
		}(i)
	}
	wg.Wait()
	return results
}

func runChild(ctx context.Context, bin string, spec childSpec) childResult {
	cmd := exec.CommandContext(ctx, bin, spec.Args...)
	var stdout strings.Builder
	cmd.Stdout = &stdout

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return childResult{Spec: spec, Err: fmt.Errorf("stderr pipe: %w", err)}
	}
	if err := cmd.Start(); err != nil {
		return childResult{Spec: spec, Err: err}
	}

	// Drain the stderr pipe to EOF before Wait: os/exec closes the pipe on
	// Wait, so reading after it would lose the child's trailing output.
	var relay sync.WaitGroup
	relay.Add(1)
	go func() {
		defer relay.Done()
		prefix := "[" + spec.Label + "] "
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			fmt.Fprintln(os.Stderr, prefix+scanner.Text())
		}
	}()
	relay.Wait()

	waitErr := cmd.Wait()
	if waitErr != nil {
		return childResult{Spec: spec, Err: waitErr}
	}

	data, err := os.ReadFile(spec.OutPath)
	if err != nil {
		return childResult{Spec: spec, Err: fmt.Errorf("read child report %s: %w", spec.OutPath, err)}
	}
	return childResult{Spec: spec, Output: data}
}
