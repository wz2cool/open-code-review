// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package merge

import (
	"reflect"
	"testing"

	"github.com/alibaba/open-code-review/internal/model"
)

func c(path string, start, end int, content, severity string) model.LlmComment {
	return model.LlmComment{
		Path:           path,
		StartLine:      start,
		EndLine:        end,
		Content:        content,
		SuggestionCode: "fix-" + path,
		Severity:       severity,
	}
}

func names(cs []model.LlmComment) (paths []string, found [][]string) {
	for _, c := range cs {
		paths = append(paths, c.Path+":"+itoa(c.StartLine))
		found = append(found, c.FoundBy)
	}
	return paths, found
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func TestMerge_MultiLineIoUBoundaries(t *testing.T) {
	similar := "the db query is missing a timeout"

	// 10-17 (8 lines) vs 12-18 (7 lines): overlap 6, union 9, IoU 2/3 > 0.6.
	merged := Merge([]Source{
		{Name: "a", Comments: []model.LlmComment{c("f.go", 10, 17, similar, "high")}},
		{Name: "b", Comments: []model.LlmComment{c("f.go", 12, 18, similar, "high")}},
	})
	if len(merged) != 1 {
		t.Fatalf("IoU 0.667 must merge, got %d findings", len(merged))
	}

	// 10-17 (8) vs 12-19 (8): overlap 6, union 10, IoU 0.6 exactly — strict
	// threshold keeps both.
	kept := Merge([]Source{
		{Name: "a", Comments: []model.LlmComment{c("f.go", 10, 17, similar, "high")}},
		{Name: "b", Comments: []model.LlmComment{c("f.go", 12, 19, similar, "high")}},
	})
	if len(kept) != 2 {
		t.Fatalf("IoU exactly 0.6 must not merge, got %d findings", len(kept))
	}

	// 10-20 vs 12-25: overlap 9, union 16, IoU 0.5625.
	wide := Merge([]Source{
		{Name: "a", Comments: []model.LlmComment{c("f.go", 10, 20, similar, "high")}},
		{Name: "b", Comments: []model.LlmComment{c("f.go", 12, 25, similar, "high")}},
	})
	if len(wide) != 2 {
		t.Fatalf("IoU 0.5625 must not merge, got %d findings", len(wide))
	}
}

func TestMerge_SingleLineRules(t *testing.T) {
	similar := "use context.WithTimeout for the db query"

	same := Merge([]Source{
		{Name: "a", Comments: []model.LlmComment{c("f.go", 10, 10, similar, "high")}},
		{Name: "b", Comments: []model.LlmComment{c("f.go", 10, 10, similar, "high")}},
	})
	if len(same) != 1 {
		t.Fatalf("same single line must merge, got %d", len(same))
	}

	diff := Merge([]Source{
		{Name: "a", Comments: []model.LlmComment{c("f.go", 10, 10, similar, "high")}},
		{Name: "b", Comments: []model.LlmComment{c("f.go", 11, 11, similar, "high")}},
	})
	if len(diff) != 2 {
		t.Fatalf("different single lines must not merge, got %d", len(diff))
	}

	// A one-line note never merges into a range finding even when the range
	// contains its line.
	cross := Merge([]Source{
		{Name: "a", Comments: []model.LlmComment{c("f.go", 10, 10, similar, "high")}},
		{Name: "b", Comments: []model.LlmComment{c("f.go", 8, 20, similar, "high")}},
	})
	if len(cross) != 2 {
		t.Fatalf("single x multi must never merge, got %d", len(cross))
	}
}

func TestMerge_ContentSimilarityGate(t *testing.T) {
	similar := "the db query is missing a timeout"

	// Same position, clearly different problems: both survive.
	different := Merge([]Source{
		{Name: "a", Comments: []model.LlmComment{c("f.go", 10, 10, "rename variable to follow style", "style")}},
		{Name: "b", Comments: []model.LlmComment{c("f.go", 10, 10, "add error handling around file read", "bug")}},
	})
	if len(different) != 2 {
		t.Fatalf("same position but different problems must not merge, got %d", len(different))
	}

	// Different positions, same problem: both survive.
	moved := Merge([]Source{
		{Name: "a", Comments: []model.LlmComment{c("f.go", 10, 10, similar, "high")}},
		{Name: "b", Comments: []model.LlmComment{c("f.go", 40, 40, similar, "high")}},
	})
	if len(moved) != 2 {
		t.Fatalf("different positions must not merge, got %d", len(moved))
	}

	// Path is part of position.
	otherFile := Merge([]Source{
		{Name: "a", Comments: []model.LlmComment{c("a.go", 10, 10, similar, "high")}},
		{Name: "b", Comments: []model.LlmComment{c("b.go", 10, 10, similar, "high")}},
	})
	if len(otherFile) != 2 {
		t.Fatalf("different files must not merge, got %d", len(otherFile))
	}
}

func TestMerge_CJKContentSimilarity(t *testing.T) {
	merged := Merge([]Source{
		{Name: "a", Comments: []model.LlmComment{c("f.go", 10, 10, "数据库查询缺少超时控制", "high")}}, // allow-non-english: CJK fixture exercising the per-rune token path
		{Name: "b", Comments: []model.LlmComment{c("f.go", 10, 10, "数据库查询缺少超时设置", "high")}}, // allow-non-english: CJK fixture exercising the per-rune token path
	})
	if len(merged) != 1 {
		t.Fatalf("similar CJK findings must merge, got %d", len(merged))
	}
}

func TestMerge_UnpositionedPreserved(t *testing.T) {
	out := Merge([]Source{
		{Name: "a", Comments: []model.LlmComment{c("f.go", 0, 0, "could not locate the code", "low")}},
		{Name: "b", Comments: []model.LlmComment{c("f.go", 0, 0, "could not locate the code", "low")}},
	})
	if len(out) != 2 {
		t.Fatalf("unpositioned findings must skip matching, got %d", len(out))
	}
	for i, want := range []string{"a", "b"} {
		if len(out[i].FoundBy) != 1 || out[i].FoundBy[0] != want {
			t.Errorf("unpositioned finding %d: found_by = %v, want [%s]", i, out[i].FoundBy, want)
		}
	}
}

func TestMerge_FoundByUnionInRegistrationOrder(t *testing.T) {
	shared := c("f.go", 10, 20, "the db query is missing a timeout", "high")
	solo := c("g.go", 3, 3, "unused import", "low")
	out := Merge([]Source{
		{Name: "gpt-5", Comments: []model.LlmComment{shared, solo}},
		{Name: "claude-opus-4-6", Comments: []model.LlmComment{c("f.go", 12, 18, "the db query is missing a timeout", "high")}},
	})
	if len(out) != 2 {
		t.Fatalf("want 2 findings, got %d", len(out))
	}
	if !reflect.DeepEqual(out[0].FoundBy, []string{"gpt-5", "claude-opus-4-6"}) {
		t.Errorf("merged finding found_by = %v", out[0].FoundBy)
	}
	if !reflect.DeepEqual(out[1].FoundBy, []string{"gpt-5"}) {
		t.Errorf("solo finding found_by = %v", out[1].FoundBy)
	}
}

func TestMerge_ConvergencePrefersSeverityThenDetail(t *testing.T) {
	// Higher severity wins regardless of order and detail.
	out := Merge([]Source{
		{Name: "a", Comments: []model.LlmComment{c("f.go", 10, 10, "the db query is missing a timeout", "medium")}},
		{Name: "b", Comments: []model.LlmComment{c("f.go", 10, 10, "the db query is missing a timeout and can hang under load", "high")}},
	})
	if len(out) != 1 || out[0].Severity != "high" {
		t.Fatalf("high severity must win, got %+v", out)
	}

	// Tie on severity: the more detailed body wins.
	tie := Merge([]Source{
		{Name: "a", Comments: []model.LlmComment{c("f.go", 10, 10, "the db query is missing a timeout", "medium")}},
		{Name: "b", Comments: []model.LlmComment{c("f.go", 10, 10, "the db query is missing a timeout which can hang", "medium")}},
	})
	if len(tie) != 1 || tie[0].Content != "the db query is missing a timeout which can hang" {
		t.Fatalf("more detailed body must win the tie, got %+v", tie)
	}

	// Suggestion and context travel with the chosen body.
	if tie[0].SuggestionCode != "fix-f.go" {
		t.Errorf("suggestion must follow the body, got %q", tie[0].SuggestionCode)
	}
}

func TestMerge_NeverFails(t *testing.T) {
	if got := Merge(nil); len(got) != 0 {
		t.Errorf("nil sources must yield empty output, got %d", len(got))
	}
	if got := Merge([]Source{{Name: "a"}}); len(got) != 0 {
		t.Errorf("empty comments must yield empty output, got %d", len(got))
	}

	single := Merge([]Source{{Name: "solo", Comments: []model.LlmComment{
		c("b.go", 1, 1, "one", "low"),
		c("a.go", 2, 4, "two", "low"),
	}}})
	if len(single) != 2 {
		t.Fatalf("single source must pass through, got %d", len(single))
	}
	for _, c := range single {
		if !reflect.DeepEqual(c.FoundBy, []string{"solo"}) {
			t.Errorf("single-source found_by = %v", c.FoundBy)
		}
	}
	// Deterministic ordering: path, then position.
	if single[0].Path != "a.go" || single[1].Path != "b.go" {
		t.Errorf("output must sort by path, got %s then %s", single[0].Path, single[1].Path)
	}
}

func TestMerge_OutputOrdering(t *testing.T) {
	out := Merge([]Source{{Name: "a", Comments: []model.LlmComment{
		c("b.go", 10, 10, "x finding", "low"),
		c("a.go", 30, 30, "y finding", "low"),
		c("a.go", 5, 8, "z finding", "low"),
		c("a.go", 0, 0, "no line", "low"),
	}}})
	want := []string{"a.go:5", "a.go:30", "a.go:0", "b.go:10"}
	paths, _ := names(out)
	for i, w := range want {
		if paths[i] != w {
			t.Fatalf("ordering mismatch at %d: got %v, want %v", i, paths, want)
		}
	}
}
