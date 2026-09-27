// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/alibaba/open-code-review/internal/llmloop"
	"github.com/alibaba/open-code-review/internal/merge"
	"github.com/alibaba/open-code-review/internal/model"
)

// childRunOutput mirrors the machine-readable fields of `ocr review -f json`
// that the merged report consumes. Single-run semantics fields (manifest,
// retry report) are deliberately not carried over.
type childRunOutput struct {
	Status    string                 `json:"status"`
	Message   string                 `json:"message"`
	LLM       *childIdentity         `json:"llm"`
	Comments  []model.LlmComment     `json:"comments"`
	Summary   *childSummary          `json:"summary"`
	SessionID string                 `json:"session_id"`
	Warnings  []llmloop.AgentWarning `json:"warnings"`
}

type childIdentity struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type childSummary struct {
	FilesReviewed int64  `json:"files_reviewed"`
	Comments      int64  `json:"comments"`
	TotalTokens   int64  `json:"total_tokens"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	Elapsed       string `json:"elapsed"`
}

// sourceReport attributes one model run inside the merged report.
type sourceReport struct {
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model"`
	Role         string `json:"role"`
	Comments     int    `json:"comments"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
	SessionID    string `json:"session_id,omitempty"`
	Elapsed      string `json:"elapsed,omitempty"`
}

type mergedSummary struct {
	FilesReviewed int64  `json:"files_reviewed"`
	Comments      int    `json:"comments"`
	TotalTokens   int64  `json:"total_tokens"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	Elapsed       string `json:"elapsed"`
}

// mergedOutput is the JSON contract of `ocr_ext review`: the ocr review
// report shape extended with found_by attribution and a sources block, minus
// the fields that only make sense for a single run (top-level session_id,
// manifest, retry report).
type mergedOutput struct {
	Status   string                 `json:"status"`
	Message  string                 `json:"message,omitempty"`
	Comments []model.LlmComment     `json:"comments"`
	Summary  mergedSummary          `json:"summary"`
	Sources  []sourceReport         `json:"sources"`
	Warnings []llmloop.AgentWarning `json:"warnings,omitempty"`
}

// buildMergedOutput combines the successful runs. Registration order (the
// primary first, then reviewers in config order) drives both the sources
// list and the found_by order. Model names come from each child's own llm
// identity so attribution follows what actually ran.
func buildMergedOutput(primary parsedChild, reviewers []parsedChild, warnings []llmloop.AgentWarning) mergedOutput {
	mergeSources := []merge.Source{{Name: primary.Model, Comments: primary.Output.Comments}}
	reports := []sourceReport{sourceReportFrom(primary, "primary")}

	// Child-run warnings (skipped files, tool failures, ...) must survive the
	// merge — they carry partial-coverage information the single-run report
	// provides. Label each with its source model.
	childWarnings := make([]llmloop.AgentWarning, 0, len(warnings))
	for _, w := range primary.Output.Warnings {
		childWarnings = append(childWarnings, llmloop.AgentWarning{Type: w.Type, File: w.File, Message: primary.Model + ": " + w.Message})
	}
	for _, r := range reviewers {
		for _, w := range r.Output.Warnings {
			childWarnings = append(childWarnings, llmloop.AgentWarning{Type: w.Type, File: w.File, Message: r.Model + ": " + w.Message})
		}
	}
	warnings = append(childWarnings, warnings...)

	var filesReviewed int64
	var totalTokens, inputTokens, outputTokens int64
	if s := primary.Output.Summary; s != nil {
		filesReviewed = s.FilesReviewed
		addTotals(s, &totalTokens, &inputTokens, &outputTokens)
	}
	for _, r := range reviewers {
		mergeSources = append(mergeSources, merge.Source{Name: r.Model, Comments: r.Output.Comments})
		reports = append(reports, sourceReportFrom(r, "reviewer"))
		addTotals(r.Output.Summary, &totalTokens, &inputTokens, &outputTokens)
		if s := r.Output.Summary; s != nil && s.FilesReviewed > filesReviewed {
			filesReviewed = s.FilesReviewed
		}
	}

	comments := merge.Merge(mergeSources)
	out := mergedOutput{
		Status:   primary.Output.Status,
		Message:  primary.Output.Message,
		Comments: comments,
		Sources:  reports,
		Warnings: warnings,
	}
	if out.Status == "" {
		out.Status = "success"
	}
	out.Summary = mergedSummary{
		FilesReviewed: filesReviewed,
		Comments:      len(comments),
		TotalTokens:   totalTokens,
		InputTokens:   inputTokens,
		OutputTokens:  outputTokens,
		Elapsed:       elapsedOf(primary.Output.Summary),
	}
	return out
}

func sourceReportFrom(p parsedChild, role string) sourceReport {
	s := sourceReport{
		Model:     p.Model,
		Role:      role,
		Comments:  len(p.Output.Comments),
		SessionID: p.Output.SessionID,
	}
	if p.Output.LLM != nil {
		s.Provider = p.Output.LLM.Provider
	}
	if cs := p.Output.Summary; cs != nil {
		s.InputTokens = cs.InputTokens
		s.OutputTokens = cs.OutputTokens
		s.TotalTokens = cs.TotalTokens
		s.Elapsed = cs.Elapsed
	}
	return s
}

func addTotals(s *childSummary, total, in, out *int64) {
	if s == nil {
		return
	}
	*total += s.TotalTokens
	*in += s.InputTokens
	*out += s.OutputTokens
}

func elapsedOf(s *childSummary) string {
	if s == nil {
		return ""
	}
	return s.Elapsed
}

func writeJSONReport(path string, m mergedOutput) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode merged report: %w", err)
	}
	return writeReport(path, append(data, '\n'))
}

func writeTextReport(path string, m mergedOutput) error {
	var b strings.Builder
	for _, c := range m.Comments {
		fmt.Fprintf(&b, "%s:%d-%d", c.Path, c.StartLine, c.EndLine)
		if c.Category != "" || c.Severity != "" {
			fmt.Fprintf(&b, " [%s/%s]", c.Category, c.Severity)
		}
		b.WriteString("\n\n")
		b.WriteString(c.Content)
		b.WriteString("\n")
		if c.SuggestionCode != "" {
			b.WriteString("\n  suggestion:\n")
			for _, line := range strings.Split(strings.TrimRight(c.SuggestionCode, "\n"), "\n") {
				b.WriteString("    " + line + "\n")
			}
		}
		if len(c.FoundBy) > 0 {
			fmt.Fprintf(&b, "\n  found by: %s\n", strings.Join(c.FoundBy, ", "))
		}
		b.WriteString("\n")
	}
	if len(m.Warnings) > 0 {
		b.WriteString("warnings:\n")
		for _, warn := range m.Warnings {
			fmt.Fprintf(&b, "  [%s] %s\n", warn.Type, warn.Message)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "summary: %d file(s) reviewed by %d model(s), %d finding(s), ~%d tokens\n",
		m.Summary.FilesReviewed, len(m.Sources), m.Summary.Comments, m.Summary.TotalTokens)
	return writeReport(path, []byte(b.String()))
}

func writeReport(path string, data []byte) error {
	if path == "" || path == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
