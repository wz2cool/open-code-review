// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// reviewerEntry is one validated entry of the config file's reviewers array.
// Raw keeps the original JSON object: entries are llm-section-shaped, so the
// temp config hands the entry to the child as its llm section verbatim and
// unknown fields survive untouched.
type reviewerEntry struct {
	Index int
	URL   string
	Model string
	Raw   []byte
}

// userConfigPath returns the config file ocr_ext reads: the same
// ~/.opencodereview/config.json that ocr uses. ocr itself never reads the
// reviewers key; it is an ocr_ext-only extension of the shared file.
func userConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".opencodereview", "config.json"), nil
}

// loadReviewers reads the config file and extracts validated reviewer
// entries. The raw map preserves every top-level field for temp-config
// generation. A missing file or missing/empty reviewers array yields zero
// entries without error; callers decide what an empty list means. A malformed
// entry or one missing url/model is an error naming the entry.
func loadReviewers(path string) (raw map[string]json.RawMessage, entries []reviewerEntry, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	spec, ok := raw["reviewers"]
	if !ok || string(spec) == "null" {
		return raw, nil, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(spec, &list); err != nil {
		return nil, nil, fmt.Errorf("parse config %s: reviewers must be an array: %w", path, err)
	}
	for i, item := range list {
		var fields struct {
			URL          string `json:"url"`
			Model        string `json:"model"`
			AuthToken    string `json:"auth_token"`
			AuthTokenCmd string `json:"auth_token_cmd"`
		}
		if err := json.Unmarshal(item, &fields); err != nil {
			return nil, nil, fmt.Errorf("parse config %s: reviewers[%d]: %w", path, i, err)
		}
		if strings.TrimSpace(fields.URL) == "" {
			return nil, nil, fmt.Errorf("reviewers[%d]: url is required", i)
		}
		if strings.TrimSpace(fields.Model) == "" {
			return nil, nil, fmt.Errorf("reviewers[%d]: model is required", i)
		}
		// Without a credential the child's llm section is incomplete and the
		// resolver would silently fall through to env endpoints — the
		// reviewer would run against something other than its own url.
		if strings.TrimSpace(fields.AuthToken) == "" && strings.TrimSpace(fields.AuthTokenCmd) == "" {
			return nil, nil, fmt.Errorf("reviewers[%d]: credentials are required: set auth_token or auth_token_cmd (reviewer entries follow the llm section, not the provider registry's api_key names)", i)
		}
		entries = append(entries, reviewerEntry{Index: i, URL: fields.URL, Model: fields.Model, Raw: item})
	}
	return raw, entries, nil
}

// buildTempConfig writes the per-reviewer config file handed to the child via
// `ocr review --config`: a copy of the user's config with the llm section
// replaced by the reviewer entry. The provider selection is removed because
// the resolver prefers it over the llm section, and the reviewers key is
// dropped because children must not fan out. The copy keeps language,
// telemetry and budget settings so every child behaves like a full standard
// run of its own. The file contains credentials and is created 0600; the
// caller removes it after the run.
func buildTempConfig(raw map[string]json.RawMessage, entry reviewerEntry) (string, error) {
	m := make(map[string]json.RawMessage, len(raw)+1)
	for k, v := range raw {
		m[k] = v
	}
	delete(m, "provider")
	delete(m, "reviewers")
	m["llm"] = json.RawMessage(entry.Raw)

	data, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("build temp config: %w", err)
	}
	tmp, err := os.CreateTemp("", fmt.Sprintf("ocr-ext-cfg-%d-*.json", os.Getpid()))
	if err != nil {
		return "", fmt.Errorf("create temp config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("write temp config: %w", err)
	}
	return tmp.Name(), nil
}
