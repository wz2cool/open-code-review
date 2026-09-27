// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/llmloop"
)

type reviewOptions struct {
	toolConfigPath        string
	rulePath              string
	repoDir               string
	from                  string
	to                    string
	commit                string
	resume                string
	excludes              string
	outputFormat          string
	audience              string
	outputPath            string
	background            string
	backgroundFile        string
	provider              string
	model                 string
	concurrency           int
	concurrentTaskTimeout int
	maxTools              int
	maxGitProcs           int
	maxTokens             int
	maxTokensBudget       int
	effort                string
	noFilter              bool
	preview               bool
}

// parentOnlyFlags are consumed by ocr_ext itself and never blindly
// forwarded. --provider/--model reach only the primary child (appended
// explicitly in runReview); forwarding them to reviewers would override the
// reviewer's own configured model.
var parentOnlyFlags = map[string]bool{
	"format":   true,
	"output":   true,
	"resume":   true,
	"preview":  true,
	"provider": true,
	"model":    true,
}

func newReviewCmd() *cobra.Command {
	opts := &reviewOptions{}
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Review a diff with the primary model plus every configured reviewer in parallel",
		Long: "Runs the standard `ocr review` once for the primary model and once per reviewer from the config " +
			"file's reviewers array, then merges the findings into a single report with per-source attribution.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReview(cmd, *opts)
		},
	}
	registerExtReviewFlags(cmd, opts)
	return cmd
}

func registerExtReviewFlags(cmd *cobra.Command, opts *reviewOptions) {
	cmd.Flags().StringVar(&opts.toolConfigPath, "tools", "", "path to JSON tools config file (default: embedded)")
	cmd.Flags().StringVar(&opts.rulePath, "rule", "", "path to JSON file with system review rules")
	cmd.Flags().StringVar(&opts.repoDir, "repo", "", "root directory of the git repository (default: current dir)")
	cmd.Flags().StringVar(&opts.from, "from", "", "source ref to start diff from (e.g., 'main')")
	cmd.Flags().StringVar(&opts.to, "to", "", "target ref to end diff at (e.g., 'feature-branch')")
	cmd.Flags().StringVarP(&opts.commit, "commit", "c", "", "single commit hash or tag to review (vs its parent)")
	cmd.Flags().StringVar(&opts.resume, "resume", "", "resume from a previous review session id (not supported by ocr_ext)")
	cmd.Flags().StringVar(&opts.excludes, "exclude", "", "comma-separated gitignore-style patterns to exclude; merged with rule.json excludes")
	cmd.Flags().StringVarP(&opts.outputFormat, "format", "f", "json", "output format of the merged report: json or text")
	cmd.Flags().StringVar(&opts.audience, "audience", "human", "output audience: human or agent")
	cmd.Flags().StringVarP(&opts.outputPath, "output", "o", "", "write the merged report to a file (default: stdout)")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 8, "max concurrent subtasks per child review")
	cmd.Flags().IntVar(&opts.concurrentTaskTimeout, "timeout", 15, "concurrent task timeout in minutes per child review")
	cmd.Flags().IntVar(&opts.maxTools, "max-tools", 0, "max tool call rounds per subtask")
	cmd.Flags().IntVar(&opts.maxGitProcs, "max-git-procs", 16, "max concurrent git subprocesses per child review")
	cmd.Flags().IntVar(&opts.maxTokens, "max-tokens", 0, "per-group prompt token ceiling per child review")
	cmd.Flags().IntVar(&opts.maxTokensBudget, "max-tokens-budget", 0, "cap total token usage per child review")
	cmd.Flags().StringVarP(&opts.background, "background", "b", "", "optional requirement/business context for the review")
	cmd.Flags().StringVarP(&opts.backgroundFile, "background-file", "B", "", "path to a Markdown file used as review background")
	cmd.Flags().StringVar(&opts.provider, "provider", "", "override the primary model's provider for this run")
	cmd.Flags().StringVar(&opts.model, "model", "", "override the primary model for this run")
	cmd.Flags().StringVar(&opts.effort, "effort", "", "review effort preset: low | medium | high")
	cmd.Flags().BoolVar(&opts.noFilter, "no-filter", false, "keep all review comments without LLM post-filtering")
	cmd.Flags().BoolVarP(&opts.preview, "preview", "p", false, "preview which files will be reviewed (not supported by ocr_ext)")
}

func validateReviewRun(opts reviewOptions) error {
	if opts.resume != "" {
		return fmt.Errorf("--resume is not supported by ocr_ext; use `ocr review --resume` for a single-model review")
	}
	if opts.preview {
		return fmt.Errorf("--preview is not supported by ocr_ext; use `ocr review --preview`")
	}
	switch opts.outputFormat {
	case "json", "text":
		return nil
	case "sarif":
		return fmt.Errorf("SARIF output is not supported by ocr_ext v1; use -f json or -f text")
	default:
		return fmt.Errorf("invalid --format %q: supported formats are json and text", opts.outputFormat)
	}
}

// forwardedArgs re-emits every flag the user actually set in the
// `--name=value` form, so bool flags survive the trip. Parent-only flags are
// excluded; children keep their own defaults for everything unset.
func forwardedArgs(cmd *cobra.Command) []string {
	var args []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if parentOnlyFlags[f.Name] {
			return
		}
		args = append(args, "--"+f.Name+"="+f.Value.String())
	})
	return args
}

func runReview(cmd *cobra.Command, opts reviewOptions) error {
	if err := validateReviewRun(opts); err != nil {
		return err
	}

	cfgPath, err := userConfigPath()
	if err != nil {
		return err
	}
	raw, entries, err := loadReviewers(cfgPath)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("no reviewers configured in %s; add a reviewers array or use `ocr review` for a single-model review", cfgPath)
	}

	// Resolve the primary endpoint up front: the duplicate check needs it,
	// and an unresolvable primary is a main-model failure, which must exit
	// with an error rather than a degraded report.
	mainEp, err := llm.ResolveEndpointWithOptions(cfgPath, llm.ResolveOptions{Provider: opts.provider, Model: opts.model})
	if err != nil {
		return err
	}

	var reviewers []reviewerEntry
	for _, e := range entries {
		if e.URL == mainEp.URL && e.Model == mainEp.Model {
			fmt.Fprintf(os.Stderr, "[ocr_ext] dropping reviewer %q: identical to the primary endpoint and model\n", e.Model)
			continue
		}
		reviewers = append(reviewers, e)
	}
	if len(reviewers) == 0 {
		return fmt.Errorf("every reviewer is identical to the primary endpoint and model; use `ocr review` for a single-model review")
	}
	// found_by is keyed by model name, so two runs sharing one name would
	// collapse into a single attribution and cross-dedup each other's
	// findings. Distinct names keep the report unambiguous.
	seenModels := map[string]bool{mainEp.Model: true}
	for _, e := range reviewers {
		if seenModels[e.Model] {
			return fmt.Errorf("duplicate reviewer model %q: reviewers must use distinct model names so found_by attribution stays unambiguous", e.Model)
		}
		seenModels[e.Model] = true
	}

	bin, err := findOCRBinary()
	if err != nil {
		return err
	}
	if warn := versionMismatch(bin); warn != "" {
		fmt.Fprintln(os.Stderr, "[ocr_ext] warning: "+warn)
	}

	primaryLabel := mainEp.Model
	if primaryLabel == "" {
		primaryLabel = "primary"
	}
	forwarded := forwardedArgs(cmd)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var cleanup []string
	defer func() {
		for _, p := range cleanup {
			os.Remove(p)
		}
	}()

	primarySpec, err := buildChildSpec(primaryLabel, forwarded, "", 0)
	if err != nil {
		return err
	}
	cleanup = append(cleanup, primarySpec.OutPath)
	// The overrides select the primary model and must reach the primary
	// child; reviewers always run with their own configured endpoint.
	if opts.provider != "" {
		primarySpec.Args = append(primarySpec.Args, "--provider="+opts.provider)
	}
	if opts.model != "" {
		primarySpec.Args = append(primarySpec.Args, "--model="+opts.model)
	}
	specs := []childSpec{primarySpec}
	for i, e := range reviewers {
		tmpCfg, err := buildTempConfig(raw, e)
		if err != nil {
			return err
		}
		cleanup = append(cleanup, tmpCfg)
		spec, err := buildChildSpec(e.Model, forwarded, tmpCfg, i+1)
		if err != nil {
			return err
		}
		cleanup = append(cleanup, spec.OutPath)
		specs = append(specs, spec)
	}

	results := runChildren(ctx, bin, specs)

	primary, err := parseChild(results[0], mainEp.Model)
	if err != nil {
		return fmt.Errorf("primary model review failed: %w", err)
	}
	var successful []parsedChild
	var warnings []llmloop.AgentWarning
	for i, res := range results[1:] {
		p, err := parseChild(res, reviewers[i].Model)
		if err != nil {
			warnings = append(warnings, llmloop.AgentWarning{
				Type:    "reviewer_failed",
				Message: fmt.Sprintf("reviewer %s failed: %v", reviewers[i].Model, err),
			})
			continue
		}
		successful = append(successful, p)
	}

	m := buildMergedOutput(primary, successful, warnings)
	if opts.outputFormat == "text" {
		return writeTextReport(opts.outputPath, m)
	}
	return writeJSONReport(opts.outputPath, m)
}

// buildChildSpec assembles one child: the review subcommand, forwarded user
// flags, a fixed JSON report target, and — for reviewers only — the temp
// config carrying the reviewer's own endpoint. The primary child gets
// neither --config nor --provider/--model here (overrides are appended by
// the caller). The report file is created by the parent with O_EXCL so its
// predictable temp name cannot be hijacked via a pre-planted symlink.
func buildChildSpec(label string, forwarded []string, cfgPath string, index int) (childSpec, error) {
	safe := strings.NewReplacer("/", "_", "\\", "_", " ", "_").Replace(label)
	out, err := os.CreateTemp("", fmt.Sprintf("ocr-ext-out-%d-%s-%d-*.json", os.Getpid(), safe, index))
	if err != nil {
		return childSpec{}, fmt.Errorf("create child report file: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(out.Name())
		return childSpec{}, fmt.Errorf("create child report file: %w", err)
	}
	args := append([]string{"review"}, forwarded...)
	if cfgPath != "" {
		args = append(args, "--config", cfgPath)
	}
	args = append(args, "-f", "json", "-o", out.Name())
	return childSpec{Label: label, Args: args, OutPath: out.Name()}, nil
}

type parsedChild struct {
	Label  string
	Model  string
	Output childRunOutput
}

// parseChild reads the child's JSON report. A failed process or an
// unparseable report is an error; the caller decides whether that means a
// hard failure (primary) or a warning (reviewer).
func parseChild(res childResult, fallbackModel string) (parsedChild, error) {
	if res.Err != nil {
		return parsedChild{}, res.Err
	}
	var out childRunOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		return parsedChild{}, fmt.Errorf("invalid JSON report: %w", err)
	}
	model := fallbackModel
	if out.LLM != nil && out.LLM.Model != "" {
		model = out.LLM.Model
	}
	return parsedChild{Label: res.Spec.Label, Model: model, Output: out}, nil
}
