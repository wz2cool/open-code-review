// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

// Package merge deterministically combines review findings from multiple
// model runs into a single list. It performs no LLM calls and never fails:
// any input yields a usable result.
//
// Two findings merge only when both conditions hold — they overlap
// positionally (same path plus multi-line ranges with IoU > 0.6, or the same
// single line; a single-line finding never matches a multi-line one) and
// their content token sets are similar. A merged finding keeps the body of
// the most severe member (ties: the more detailed content) and carries the
// union of contributing source names in registration order.
package merge

import (
	"sort"
	"strings"
	"unicode"

	"github.com/alibaba/open-code-review/internal/model"
)

// Source is one model run's findings. Name labels the run in FoundBy output;
// the order of Sources defines the order of names there.
type Source struct {
	Name     string
	Comments []model.LlmComment
}

// contentSimilarityThreshold is the Jaccard similarity below which two
// same-position findings are treated as different problems.
const contentSimilarityThreshold = 0.5

// positionIoUThreshold is the multi-line overlap ratio below which two
// findings are treated as occupying different positions.
const positionIoUThreshold = 0.6

// Merge combines every source's comments as described in the package doc.
// Findings without a resolvable line number skip matching entirely and are
// preserved as-is.
//
// Grouping is single-linkage and order-sensitive: a finding joins the first
// existing group containing any member it matches, so a transitive chain
// (A matches B, B matches C, but A and C do not match directly) forms one
// group. This is accepted so bridged phrasings of the same problem converge;
// the similarity gate keeps chains rare.
func Merge(sources []Source) []model.LlmComment {
	type group struct {
		members []model.LlmComment
		foundBy []string
	}

	var (
		groups       []group
		unpositioned []model.LlmComment
	)
	for _, src := range sources {
		for _, c := range src.Comments {
			if _, _, ok := commentLines(c); !ok {
				c.FoundBy = []string{src.Name}
				unpositioned = append(unpositioned, c)
				continue
			}
			joined := false
			for gi := range groups {
				for _, m := range groups[gi].members {
					if match(c, m) {
						groups[gi].members = append(groups[gi].members, c)
						groups[gi].foundBy = appendUnique(groups[gi].foundBy, src.Name)
						joined = true
						break
					}
				}
				if joined {
					break
				}
			}
			if !joined {
				groups = append(groups, group{members: []model.LlmComment{c}, foundBy: []string{src.Name}})
			}
		}
	}

	out := make([]model.LlmComment, 0, len(groups)+len(unpositioned))
	for _, g := range groups {
		out = append(out, collapse(g.members, g.foundBy))
	}
	out = append(out, unpositioned...)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// match reports whether two findings describe the same problem. Both
// conditions are required: position overlap alone would merge different
// problems on the same line (two single-line comments always overlap
// completely), content similarity alone would merge the same problem at
// different positions.
func match(a, b model.LlmComment) bool {
	if a.Path != b.Path {
		return false
	}
	if !positionMatch(a, b) {
		return false
	}
	return jaccard(contentTokens(a.Content), contentTokens(b.Content)) >= contentSimilarityThreshold
}

// commentLines normalizes a finding's position. Findings without a usable
// start line cannot be located and are reported as not ok.
func commentLines(c model.LlmComment) (start, end int, ok bool) {
	if c.StartLine <= 0 {
		return 0, 0, false
	}
	start = c.StartLine
	end = c.EndLine
	if end < start {
		end = start
	}
	return start, end, true
}

func positionMatch(a, b model.LlmComment) bool {
	as, ae, aok := commentLines(a)
	bs, be, bok := commentLines(b)
	if !aok || !bok {
		return false
	}
	aMulti, bMulti := ae > as, be > bs
	if aMulti != bMulti {
		// A one-line note and a range finding rarely describe the same
		// problem; treating them as incomparable avoids swallowing range
		// findings into whatever shares their first line.
		return false
	}
	if !aMulti {
		return as == bs
	}
	overlap := min(ae, be) - max(as, bs) + 1
	if overlap <= 0 {
		return false
	}
	union := (ae - as + 1) + (be - bs + 1) - overlap
	return float64(overlap)/float64(union) > positionIoUThreshold
}

// contentTokens splits content into a comparison token set: ASCII
// alphanumeric runs act as words, while every non-ASCII letter (Han, kana,
// Hangul, Cyrillic, accented Latin, ...) becomes its own token because those
// scripts do not separate words.
func contentTokens(s string) map[string]struct{} {
	tokens := make(map[string]struct{})
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			tokens[word.String()] = struct{}{}
			word.Reset()
		}
	}
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			word.WriteRune(r)
		case r >= 0x80 && unicode.IsLetter(r):
			flush()
			tokens[string(r)] = struct{}{}
		default:
			flush()
		}
	}
	flush()
	return tokens
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	small, large := a, b
	if len(small) > len(large) {
		small, large = large, small
	}
	inter := 0
	for t := range small {
		if _, ok := large[t]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

func severityRank(severity string) int {
	switch severity {
	case "critical":
		return 3
	case "high":
		return 2
	case "medium":
		return 1
	case "low":
		return 0
	}
	return -1
}

// collapse picks the group's body: highest severity wins, ties go to the
// more detailed content, then to the earlier source. Suggestion and context
// travel with the body rather than being stitched across members.
func collapse(members []model.LlmComment, foundBy []string) model.LlmComment {
	best := members[0]
	for _, m := range members[1:] {
		if severityRank(m.Severity) > severityRank(best.Severity) ||
			(severityRank(m.Severity) == severityRank(best.Severity) &&
				len([]rune(m.Content)) > len([]rune(best.Content))) {
			best = m
		}
	}
	best.FoundBy = foundBy
	return best
}

func appendUnique(names []string, name string) []string {
	for _, n := range names {
		if n == name {
			return names
		}
	}
	return append(names, name)
}

// less orders merged output by path, then position. Findings without a line
// number sort after positioned ones of the same path; across different paths
// they are ordered by path like everything else.
func less(a, b model.LlmComment) bool {
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	as, ae, aok := commentLines(a)
	bs, be, bok := commentLines(b)
	if aok != bok {
		return aok
	}
	if !aok {
		return false
	}
	if as != bs {
		return as < bs
	}
	return ae < be
}
