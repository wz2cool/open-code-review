// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/llmloop"
	"github.com/alibaba/open-code-review/internal/model"
)

// setTestHome points the OCR home at a temp dir so tests never touch the
// developer's real ~/.opencodereview/config.json.
func setTestHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

const fakeOCRScript = `#!/bin/sh
out=""
has_config=0
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  case "$a" in --config|--config=*) has_config=1 ;; esac
  prev="$a"
done
echo "[fake] review starting" >&2
if [ "$FAKE_MODE" = "fail_reviewer" ] && [ "$has_config" = "1" ]; then
  echo "reviewer boom" >&2
  exit 3
fi
if [ "$FAKE_MODE" = "fail_primary" ] && [ "$has_config" = "0" ]; then
  echo "primary boom" >&2
  exit 4
fi
if [ "$1" = "version" ]; then
  echo "$FAKE_CHILD_VERSION"
  exit 0
fi
if [ "$has_config" = "1" ]; then
  printf '%s' "$FAKE_REVIEWER_JSON" > "$out"
else
  printf '%s' "$FAKE_PRIMARY_JSON" > "$out"
fi
`

const testPrimaryJSON = `{"status":"success","llm":{"provider":"fake","model":"gpt-5"},` +
	`"comments":[{"path":"f.go","start_line":10,"end_line":20,"content":"the db query is missing a timeout","severity":"high","suggestion_code":"S1"}],` +
	`"summary":{"files_reviewed":3,"comments":1,"total_tokens":100,"input_tokens":80,"output_tokens":20,"elapsed":"5s"},` +
	`"session_id":"sess-primary"}`

const testReviewerJSON = `{"status":"success","llm":{"provider":"fake","model":"claude-x"},` +
	`"comments":[` +
	`{"path":"f.go","start_line":12,"end_line":18,"content":"the db query is missing a timeout here","severity":"high","suggestion_code":"S2"},` +
	`{"path":"g.go","start_line":3,"end_line":3,"content":"unused import","severity":"low","suggestion_code":"S3"}],` +
	`"summary":{"files_reviewed":3,"comments":2,"total_tokens":50,"input_tokens":40,"output_tokens":10,"elapsed":"7s"},` +
	`"warnings":[{"type":"tool_failure","message":"tool x failed"}],` +
	`"session_id":"sess-reviewer"}`

func writeTestConfig(t *testing.T, reviewers string) string {
	t.Helper()
	home := t.TempDir()
	setTestHome(t, home)
	cfgDir := filepath.Join(home, ".opencodereview")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "config.json")
	cfg := `{"llm":{"url":"https://fake.test/v1","auth_token":"tok","model":"gpt-5"},"language":"en","reviewers":` + reviewers + `}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func installFakeOCR(t *testing.T, extra string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake ocr child is a POSIX shell script")
	}
	dir := t.TempDir()
	script := fakeOCRScript
	if extra != "" {
		script = strings.Replace(script, `echo "[fake] review starting" >&2`, `echo "[fake] review starting" >&2`+"\n"+extra, 1)
	}
	path := filepath.Join(dir, "ocr")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ocr: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// runTestReview drives runReview with a fake child binary and returns the
// merged JSON the command wrote to its output file.
func runTestReview(t *testing.T) mergedOutput {
	t.Helper()
	writeTestConfig(t, `[{"url":"https://fake.test/v1","auth_token":"tok","model":"claude-x"}]`)
	installFakeOCR(t, "")
	t.Setenv("FAKE_PRIMARY_JSON", testPrimaryJSON)
	t.Setenv("FAKE_REVIEWER_JSON", testReviewerJSON)
	t.Setenv("FAKE_MODE", "")
	// Keep the host's OCR_LLM_* env from shadowing the test config.
	t.Setenv("OCR_LLM_URL", "")
	t.Setenv("OCR_LLM_TOKEN", "")
	t.Setenv("OCR_LLM_MODEL", "")

	outPath := filepath.Join(t.TempDir(), "merged.json")
	opts := reviewOptions{outputFormat: "json", outputPath: outPath}
	if err := runReview(newReviewCmd(), opts); err != nil {
		t.Fatalf("runReview: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read merged report: %v", err)
	}
	var m mergedOutput
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse merged report: %v", err)
	}
	return m
}

func TestRunReview_MergesChildOutputs(t *testing.T) {
	m := runTestReview(t)

	if m.Status != "success" {
		t.Errorf("status = %q", m.Status)
	}
	if len(m.Comments) != 2 {
		t.Fatalf("want 2 merged findings, got %d", len(m.Comments))
	}
	first := m.Comments[0]
	if first.Path != "f.go" || first.StartLine != 12 || first.EndLine != 18 {
		t.Errorf("merged body position = %s:%d-%d", first.Path, first.StartLine, first.EndLine)
	}
	if !reflect.DeepEqual(first.FoundBy, []string{"gpt-5", "claude-x"}) {
		t.Errorf("merged found_by = %v", first.FoundBy)
	}
	if first.SuggestionCode != "S2" {
		t.Errorf("body suggestion = %q, want the more detailed body's S2", first.SuggestionCode)
	}
	if !reflect.DeepEqual(m.Comments[1].FoundBy, []string{"claude-x"}) {
		t.Errorf("solo found_by = %v", m.Comments[1].FoundBy)
	}

	if len(m.Sources) != 2 {
		t.Fatalf("want 2 sources, got %d", len(m.Sources))
	}
	if m.Sources[0].Model != "gpt-5" || m.Sources[0].Role != "primary" || m.Sources[0].SessionID != "sess-primary" {
		t.Errorf("primary source = %+v", m.Sources[0])
	}
	if m.Sources[1].Model != "claude-x" || m.Sources[1].Role != "reviewer" || m.Sources[1].Comments != 2 {
		t.Errorf("reviewer source = %+v", m.Sources[1])
	}
	if m.Summary.TotalTokens != 150 || m.Summary.InputTokens != 120 || m.Summary.OutputTokens != 30 {
		t.Errorf("aggregated tokens = %+v", m.Summary)
	}
	if m.Summary.Comments != 2 || m.Summary.FilesReviewed != 3 || m.Summary.Elapsed != "5s" {
		t.Errorf("summary = %+v", m.Summary)
	}
	if len(m.Warnings) != 1 || m.Warnings[0].Message != "claude-x: tool x failed" {
		t.Errorf("child warnings must be propagated and labeled, got %+v", m.Warnings)
	}
}

func TestRunReview_PrimaryFailureFails(t *testing.T) {
	writeTestConfig(t, `[{"url":"https://fake.test/v1","auth_token":"tok","model":"claude-x"}]`)
	installFakeOCR(t, "")
	t.Setenv("FAKE_PRIMARY_JSON", testPrimaryJSON)
	t.Setenv("FAKE_REVIEWER_JSON", testReviewerJSON)
	t.Setenv("FAKE_MODE", "fail_primary")

	err := runReview(newReviewCmd(), reviewOptions{outputFormat: "json", outputPath: filepath.Join(t.TempDir(), "m.json")})
	if err == nil || !strings.Contains(err.Error(), "primary model review failed") || !strings.Contains(err.Error(), "exit status 4") {
		t.Fatalf("primary failure must error, got %v", err)
	}
}

func TestRunReview_ReviewerFailureDegrades(t *testing.T) {
	writeTestConfig(t, `[{"url":"https://fake.test/v1","auth_token":"tok","model":"claude-x"}]`)
	installFakeOCR(t, "")
	t.Setenv("FAKE_PRIMARY_JSON", testPrimaryJSON)
	t.Setenv("FAKE_REVIEWER_JSON", testReviewerJSON)
	t.Setenv("FAKE_MODE", "fail_reviewer")
	t.Setenv("OCR_LLM_URL", "")
	t.Setenv("OCR_LLM_TOKEN", "")
	t.Setenv("OCR_LLM_MODEL", "")

	outPath := filepath.Join(t.TempDir(), "merged.json")
	if err := runReview(newReviewCmd(), reviewOptions{outputFormat: "json", outputPath: outPath}); err != nil {
		t.Fatalf("reviewer failure must degrade to exit 0, got %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read merged report: %v", err)
	}
	var m mergedOutput
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse merged report: %v", err)
	}
	if len(m.Comments) != 1 || m.Comments[0].Path != "f.go" {
		t.Fatalf("only the primary's findings survive, got %+v", m.Comments)
	}
	if len(m.Warnings) != 1 || m.Warnings[0].Type != "reviewer_failed" || !strings.Contains(m.Warnings[0].Message, "claude-x") {
		t.Fatalf("reviewer failure must become a warning, got %+v", m.Warnings)
	}
	if len(m.Sources) != 1 || m.Sources[0].Model != "gpt-5" {
		t.Fatalf("failed reviewer must not appear in sources, got %+v", m.Sources)
	}
}

func TestValidateReviewRun(t *testing.T) {
	cases := []struct {
		name    string
		opts    reviewOptions
		wantErr string
	}{
		{name: "json ok", opts: reviewOptions{outputFormat: "json"}},
		{name: "text ok", opts: reviewOptions{outputFormat: "text"}},
		{name: "sarif rejected", opts: reviewOptions{outputFormat: "sarif"}, wantErr: "SARIF output is not supported"},
		{name: "invalid format", opts: reviewOptions{outputFormat: "xml"}, wantErr: "invalid --format"},
		{name: "resume rejected", opts: reviewOptions{outputFormat: "json", resume: "s1"}, wantErr: "--resume is not supported"},
		{name: "preview rejected", opts: reviewOptions{outputFormat: "json", preview: true}, wantErr: "--preview is not supported"},
	}
	for _, tc := range cases {
		err := validateReviewRun(tc.opts)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: error = %v, want it to contain %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestLoadReviewers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if _, entries, err := loadReviewers(filepath.Join(dir, "absent.json")); err != nil || len(entries) != 0 {
		t.Fatalf("missing file: entries = %d, err = %v", len(entries), err)
	}

	if err := os.WriteFile(path, []byte(`{"llm":{"url":"https://x/v1","model":"m"}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, entries, err := loadReviewers(path); err != nil || len(entries) != 0 {
		t.Fatalf("no reviewers key: entries = %d, err = %v", len(entries), err)
	}

	if err := os.WriteFile(path, []byte(`{"reviewers":[{"model":"m"}]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := loadReviewers(path); err == nil || !strings.Contains(err.Error(), "reviewers[0]: url is required") {
		t.Fatalf("missing url must name the entry, got %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"reviewers":[{"url":"https://x/v1","auth_token":"t","model":"m1"},{"url":"https://y/v1","auth_token":"t","model":"m2"}]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, entries, err := loadReviewers(path)
	if err != nil || len(entries) != 2 || entries[0].Model != "m1" || entries[1].Model != "m2" {
		t.Fatalf("entries = %+v, err = %v", entries, err)
	}
}

func TestBuildTempConfig(t *testing.T) {
	raw := map[string]json.RawMessage{
		"provider":  json.RawMessage(`"openai"`),
		"language":  json.RawMessage(`"ja-JP"`),
		"reviewers": json.RawMessage(`[]`),
	}
	entry := reviewerEntry{Index: 0, URL: "https://r.test/v1", Model: "r-model", Raw: json.RawMessage(`{"url":"https://r.test/v1","auth_token":"tok","model":"r-model","custom_future_field":1}`)}

	path, err := buildTempConfig(raw, entry)
	if err != nil {
		t.Fatalf("buildTempConfig: %v", err)
	}
	defer os.Remove(path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("temp config perms = %v, want 0600 (it carries credentials)", info.Mode().Perm())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := got["provider"]; ok {
		t.Error("provider selection must be removed so the llm section wins")
	}
	if _, ok := got["reviewers"]; ok {
		t.Error("reviewers must be removed from the child config")
	}
	if _, ok := got["language"]; !ok {
		t.Error("unrelated fields must be preserved (the child is a full standard run)")
	}
	var llmSection struct {
		Model             string `json:"model"`
		CustomFutureField int    `json:"custom_future_field"`
	}
	if err := json.Unmarshal(got["llm"], &llmSection); err != nil {
		t.Fatalf("parse llm section: %v", err)
	}
	if llmSection.Model != "r-model" || llmSection.CustomFutureField != 1 {
		t.Errorf("llm section = %+v, want the reviewer entry verbatim", llmSection)
	}
}

func TestDropReviewerIdenticalToPrimary(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := `{"llm":{"url":"https://fake.test/v1","auth_token":"tok","model":"gpt-5"},` +
		`"reviewers":[{"url":"https://fake.test/v1","auth_token":"tok","model":"gpt-5"},{"url":"https://other.test/v1","auth_token":"t","model":"m2"}]}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("OCR_LLM_URL", "")
	t.Setenv("OCR_LLM_TOKEN", "")
	t.Setenv("OCR_LLM_MODEL", "")

	mainEp, err := llm.ResolveEndpointWithOptions(cfgPath, llm.ResolveOptions{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	_, entries, err := loadReviewers(cfgPath)
	if err != nil {
		t.Fatalf("loadReviewers: %v", err)
	}
	var kept []string
	for _, e := range entries {
		if e.URL == mainEp.URL && e.Model == mainEp.Model {
			continue
		}
		kept = append(kept, e.Model)
	}
	if len(kept) != 1 || kept[0] != "m2" {
		t.Fatalf("kept = %v, want only the distinct reviewer", kept)
	}
}

func TestVersionMismatch(t *testing.T) {
	old := Version
	defer func() { Version = old }()

	if Version == "dev" || Version == "" {
		Version = "v1.2.3"
		defer func() { Version = "dev" }()
	}

	dir := t.TempDir()
	mismatch := filepath.Join(dir, "ocr")
	if err := os.WriteFile(mismatch, []byte("#!/bin/sh\necho \"open-code-review v9.9.9 darwin/amd64\"\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if warn := versionMismatch(mismatch); warn == "" || !strings.Contains(warn, "v9.9.9") {
		t.Errorf("version drift must warn, got %q", warn)
	}

	matching := filepath.Join(dir, "ocr-match")
	if err := os.WriteFile(matching, []byte("#!/bin/sh\necho \"open-code-review v1.2.3 darwin/amd64\"\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if warn := versionMismatch(matching); warn != "" {
		t.Errorf("matching version must not warn, got %q", warn)
	}
}

func TestFindOCRBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake binary is a shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ocr"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	found, err := findOCRBinary()
	if err != nil {
		t.Fatalf("findOCRBinary: %v", err)
	}
	if filepath.Base(found) != "ocr" {
		t.Errorf("found = %s", found)
	}

	empty := t.TempDir()
	t.Setenv("PATH", empty)
	if _, err := exec.LookPath("ocr"); err == nil {
		t.Skip("a real ocr is on this machine's PATH and cannot be excluded")
	}
	if _, err := findOCRBinary(); err == nil {
		t.Fatal("no ocr anywhere must error")
	}
}

func TestForwardedArgsExcludeParentOnlyFlags(t *testing.T) {
	cmd := newReviewCmd()
	if err := cmd.ParseFlags([]string{"--from", "main", "--to", "HEAD", "--no-filter=true", "--audience", "agent", "--format", "json", "--resume", "s1"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	got := forwardedArgs(cmd)
	joined := strings.Join(got, " ")
	for _, want := range []string{"--from=main", "--to=HEAD", "--no-filter=true", "--audience=agent"} {
		if !strings.Contains(joined, want) {
			t.Errorf("forwarded args missing %q: %v", want, got)
		}
	}
	for _, banned := range []string{"--format", "--resume", "--output"} {
		if strings.Contains(joined, banned) {
			t.Errorf("forwarded args must not contain parent-only %q: %v", banned, got)
		}
	}
}

func TestRunReview_WithoutReviewersErrors(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	cfgDir := filepath.Join(home, ".opencodereview")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"llm":{"url":"https://fake.test/v1","auth_token":"tok","model":"gpt-5"}}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	installFakeOCR(t, "")

	err := runReview(newReviewCmd(), reviewOptions{outputFormat: "json"})
	if err == nil || !strings.Contains(err.Error(), "no reviewers configured") {
		t.Fatalf("empty reviewers must error with guidance, got %v", err)
	}
}

func TestRunChildRelaysStderrWithPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ocr child is a POSIX shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"line one\" >&2\necho \"line two\" >&2\nprintf '{}' > \"$3\"\n"
	bin := filepath.Join(dir, "ocr")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := filepath.Join(t.TempDir(), "relay.json")

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	res := runChild(context.Background(), bin, childSpec{Label: "gpt-5", Args: []string{"review", "-o", out}, OutPath: out})
	w.Close()
	os.Stderr = old
	relay := readAll(t, r)

	if res.Err != nil {
		t.Fatalf("runChild: %v", res.Err)
	}
	for _, want := range []string{"[gpt-5] line one", "[gpt-5] line two"} {
		if !strings.Contains(relay, want) {
			t.Errorf("relayed stderr missing %q, got %q", want, relay)
		}
	}
}

func readAll(t *testing.T, r *os.File) string {
	t.Helper()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

func TestWriteTextReport(t *testing.T) {
	m := mergedOutput{
		Status: "success",
		Comments: []model.LlmComment{{
			Path:           "f.go",
			StartLine:      10,
			EndLine:        20,
			Content:        "the db query is missing a timeout",
			SuggestionCode: "ctx, cancel := context.WithTimeout(ctx)\ndefer cancel()",
			Severity:       "high",
			Category:       "bug",
			FoundBy:        []string{"gpt-5", "claude-x"},
		}},
		Summary:  mergedSummary{FilesReviewed: 3, Comments: 1, TotalTokens: 150},
		Sources:  []sourceReport{{Model: "gpt-5", Role: "primary"}, {Model: "claude-x", Role: "reviewer"}},
		Warnings: []llmloop.AgentWarning{{Type: "reviewer_failed", Message: "reviewer claude-x failed: exit status 3"}},
	}
	path := filepath.Join(t.TempDir(), "out.txt")
	if err := writeTextReport(path, m); err != nil {
		t.Fatalf("writeTextReport: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"f.go:10-20 [bug/high]",
		"the db query is missing a timeout",
		"    ctx, cancel := context.WithTimeout(ctx)",
		"    defer cancel()",
		"  found by: gpt-5, claude-x",
		"[reviewer_failed] reviewer claude-x failed: exit status 3",
		"reviewed by 2 model(s), 1 finding(s), ~150 tokens",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("text report missing %q:\n%s", want, got)
		}
	}
}

func TestRunReview_CleansUpTempFiles(t *testing.T) {
	runTestReview(t)
	outPattern := filepath.Join(os.TempDir(), fmt.Sprintf("ocr-ext-out-%d-*", os.Getpid()))
	cfgPattern := filepath.Join(os.TempDir(), fmt.Sprintf("ocr-ext-cfg-%d-*", os.Getpid()))
	// Children run with the test process's pid in their temp names; the
	// parent removes everything it created, so nothing may remain.
	if matches, _ := filepath.Glob(outPattern); len(matches) != 0 {
		t.Errorf("child output files must be removed, got %v", matches)
	}
	if matches, _ := filepath.Glob(cfgPattern); len(matches) != 0 {
		t.Errorf("temp config files must be removed, got %v", matches)
	}
}

func TestRootCmd_VersionSubcommand(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "v9.8.7-test"

	root := newRootCmd()
	root.SetArgs([]string{"version"})
	root.SetOut(nil)
	root.SetErr(nil)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute version: %v", err)
	}
}

func TestWriteJSONReport_File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.json")
	m := mergedOutput{Status: "success", Comments: []model.LlmComment{}}
	if err := writeJSONReport(path, m); err != nil {
		t.Fatalf("writeJSONReport: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `"status": "success"`) {
		t.Errorf("json report = %s", data)
	}
}

func TestParseChild_InvalidJSON(t *testing.T) {
	_, err := parseChild(childResult{Spec: childSpec{Label: "x"}, Output: []byte("{not json")}, "m")
	if err == nil || !strings.Contains(err.Error(), "invalid JSON report") {
		t.Fatalf("invalid JSON must error, got %v", err)
	}
}

func TestLoadReviewers_ErrorBranches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := loadReviewers(path); err == nil || !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("malformed config must error, got %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"reviewers":"not-an-array"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := loadReviewers(path); err == nil || !strings.Contains(err.Error(), "reviewers must be an array") {
		t.Fatalf("non-array reviewers must error, got %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"reviewers":[42]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := loadReviewers(path); err == nil || !strings.Contains(err.Error(), "reviewers[0]") {
		t.Fatalf("non-object entry must name the index, got %v", err)
	}
}

func TestVersionMismatch_UnreadableChild(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "v1.2.3"

	dir := t.TempDir()
	bad := filepath.Join(dir, "ocr")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	if warn := versionMismatch(bad); warn == "" || !strings.Contains(warn, "could not read") {
		t.Errorf("unreadable child version must warn, got %q", warn)
	}
}

func TestBuildChildSpec_SanitizedExclusiveReportFile(t *testing.T) {
	spec, err := buildChildSpec("model/with slash and space", []string{"--from=main"}, "/tmp/cfg.json", 3)
	if err != nil {
		t.Fatalf("buildChildSpec: %v", err)
	}
	defer os.Remove(spec.OutPath)
	base := filepath.Base(spec.OutPath)
	if strings.ContainsAny(base, "/ ") {
		t.Errorf("report path label not sanitized: %s", base)
	}
	if !strings.HasPrefix(base, fmt.Sprintf("ocr-ext-out-%d-", os.Getpid())) || !strings.Contains(base, "-3-") || !strings.HasSuffix(base, ".json") {
		t.Errorf("report path = %s, want pid-prefixed and index-suffixed", base)
	}
	// The report file must already exist (O_EXCL via CreateTemp) so a local
	// attacker cannot pre-plant a symlink at the predictable name.
	if _, err := os.Stat(spec.OutPath); err != nil {
		t.Errorf("report file must be created by the parent: %v", err)
	}
	joined := strings.Join(spec.Args, " ")
	for _, want := range []string{"review ", "--from=main", "--config /tmp/cfg.json", "-f json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("child args missing %q: %v", want, spec.Args)
		}
	}

	// A second spec with the same label/index gets its own file, not the
	// same path (CreateTemp is exclusive).
	other, err := buildChildSpec("model/with slash and space", nil, "", 3)
	if err != nil {
		t.Fatalf("buildChildSpec: %v", err)
	}
	defer os.Remove(other.OutPath)
	if other.OutPath == spec.OutPath {
		t.Error("two children sharing a label must not share a report file")
	}
}

func TestLoadReviewers_MissingCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"reviewers":[{"url":"https://x/v1","model":"m"}]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := loadReviewers(path); err == nil || !strings.Contains(err.Error(), "credentials are required") {
		t.Fatalf("credential-less entry must error, got %v", err)
	}
}

func TestRunReview_DuplicateModelNamesError(t *testing.T) {
	writeTestConfig(t, `[{"url":"https://fake.test/v1","auth_token":"tok","model":"claude-x"},{"url":"https://other.test/v1","auth_token":"tok","model":"claude-x"}]`)
	installFakeOCR(t, "")
	t.Setenv("FAKE_PRIMARY_JSON", testPrimaryJSON)
	t.Setenv("FAKE_REVIEWER_JSON", testReviewerJSON)
	t.Setenv("FAKE_MODE", "")
	t.Setenv("OCR_LLM_URL", "")
	t.Setenv("OCR_LLM_TOKEN", "")
	t.Setenv("OCR_LLM_MODEL", "")
	err := runReview(newReviewCmd(), reviewOptions{outputFormat: "json", outputPath: filepath.Join(t.TempDir(), "m.json")})
	if err == nil || !strings.Contains(err.Error(), "duplicate reviewer model") {
		t.Fatalf("duplicate model names must error, got %v", err)
	}
}

func TestRunChild_StartError(t *testing.T) {
	res := runChild(context.Background(), filepath.Join(t.TempDir(), "no-such-binary"), childSpec{Label: "x", OutPath: filepath.Join(t.TempDir(), "o.json")})
	if res.Err == nil {
		t.Fatal("a nonexistent child binary must error")
	}
}

func TestBuildTempConfig_UnwritableTempDir(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "no-such-dir"))
	if _, err := buildTempConfig(map[string]json.RawMessage{}, reviewerEntry{Raw: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("an unwritable temp dir must error")
	}
}

func TestCandidateNames_IncludeExeVariants(t *testing.T) {
	names := candidateNames()
	for _, want := range []string{"ocr", "opencodereview", "ocr.exe", "opencodereview.exe"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("candidateNames missing %q: %v", want, names)
		}
	}
}

func TestWriteReport_Stdout(t *testing.T) {
	if err := writeReport("", nil); err != nil {
		t.Fatalf("writeReport to stdout: %v", err)
	}
}

func TestUserConfigPath_ErrorWhenHomeUnknown(t *testing.T) {
	old, had := os.LookupEnv("HOME")
	t.Cleanup(func() {
		if had {
			os.Setenv("HOME", old)
		} else {
			os.Unsetenv("HOME")
		}
	})
	os.Setenv("HOME", "")
	if _, err := userConfigPath(); err == nil {
		t.Fatal("an unknown home directory must error")
	}
}

func TestRunReview_PrimaryOverridesReachChild(t *testing.T) {
	// The fake ocr records its argv so the test can assert the overrides were
	// appended to the primary child's command line.
	argvDir := t.TempDir()
	writeTestConfig(t, `[{"url":"https://fake.test/v1","auth_token":"tok","model":"claude-x"}]`)
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"version\" ]; then echo \"$FAKE_CHILD_VERSION\"; exit 0; fi\n" +
		"has_config=0\nprev=\"\"\nout=\"\"\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"-o\" ]; then out=\"$a\"; fi\n" +
		"  case \"$a\" in --config|--config=*) has_config=1 ;; esac\n" +
		"  prev=\"$a\"\n" +
		"done\n" +
		"if [ \"$has_config\" = \"1\" ]; then\n" +
		"  printf '%s\\n' \"$@\" > \"$ARGV_REVIEWER\"\n" +
		"  printf '%s' \"$FAKE_REVIEWER_JSON\" > \"$out\"\n" +
		"else\n" +
		"  printf '%s\\n' \"$@\" > \"$ARGV_PRIMARY\"\n" +
		"  printf '%s' \"$FAKE_PRIMARY_JSON\" > \"$out\"\n" +
		"fi\n"
	bin := filepath.Join(dir, "ocr")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_PRIMARY_JSON", testPrimaryJSON)
	t.Setenv("FAKE_REVIEWER_JSON", testReviewerJSON)
	t.Setenv("FAKE_MODE", "")
	t.Setenv("FAKE_CHILD_VERSION", "")
	t.Setenv("OCR_LLM_URL", "")
	t.Setenv("OCR_LLM_TOKEN", "")
	t.Setenv("OCR_LLM_MODEL", "")
	t.Setenv("ARGV_PRIMARY", filepath.Join(argvDir, "primary.argv"))
	t.Setenv("ARGV_REVIEWER", filepath.Join(argvDir, "reviewer.argv"))

	// A --model override resolves against the config's llm section; a
	// --provider override would need a registry entry the temp-home config
	// does not have, so this test pins model-only.
	cmd := newReviewCmd()
	cmd.SetArgs([]string{"--model", "gpt-5-turbo"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	primary, err := os.ReadFile(filepath.Join(argvDir, "primary.argv"))
	if err != nil {
		t.Fatalf("primary argv not recorded: %v", err)
	}
	primaryArgs := strings.Fields(string(primary))
	if !containsArg(primaryArgs, "--model=gpt-5-turbo") {
		t.Errorf("primary child args missing the model override: %v", primaryArgs)
	}
	if containsArg(primaryArgs, "--config") {
		t.Errorf("primary child must not carry --config: %v", primaryArgs)
	}

	reviewer, err := os.ReadFile(filepath.Join(argvDir, "reviewer.argv"))
	if err != nil {
		t.Fatalf("reviewer argv not recorded: %v", err)
	}
	reviewerArgs := strings.Fields(string(reviewer))
	if containsArg(reviewerArgs, "--model=gpt-5-turbo") {
		t.Errorf("reviewer child must not carry primary overrides: %v", reviewerArgs)
	}
	if !containsArg(reviewerArgs, "--config") {
		t.Errorf("reviewer child must carry --config: %v", reviewerArgs)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want || strings.HasPrefix(a, want+"=") || (want == "--config" && a == "--config") {
			return true
		}
	}
	return false
}

func TestRunReview_AudienceForwardedToChildren(t *testing.T) {
	writeTestConfig(t, `[{"url":"https://fake.test/v1","auth_token":"tok","model":"claude-x"}]`)
	installFakeOCR(t, "")
	t.Setenv("FAKE_PRIMARY_JSON", testPrimaryJSON)
	t.Setenv("FAKE_REVIEWER_JSON", testReviewerJSON)
	t.Setenv("FAKE_MODE", "")
	t.Setenv("OCR_LLM_URL", "")
	t.Setenv("OCR_LLM_TOKEN", "")
	t.Setenv("OCR_LLM_MODEL", "")

	cmd := newReviewCmd()
	cmd.SetArgs([]string{"--audience", "agent", "-o", filepath.Join(t.TempDir(), "m.json")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Forwarding itself is asserted by TestForwardedArgsExcludeParentOnlyFlags;
	// this test pins the end-to-end path: --audience=agent is accepted and the
	// run completes with both children.
}
