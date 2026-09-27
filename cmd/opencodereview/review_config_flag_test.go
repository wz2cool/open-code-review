// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/llm"
)

func TestReviewConfigFlag_Registered(t *testing.T) {
	flag := reviewCmd.Flags().Lookup("config")
	if flag == nil {
		t.Fatal("--config flag is not registered on ocr review")
	}
	if flag.DefValue != "" {
		t.Errorf("--config default = %q, want empty so the default path behavior is preserved", flag.DefValue)
	}
}

func TestExecuteReviewConfig_MissingFileRejected(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-config.json")
	err := executeReviewContext(context.Background(), reviewOptions{configPath: missing})
	if err == nil || !strings.Contains(err.Error(), "--config") {
		t.Fatalf("missing --config file must fail with a --config-prefixed error, got %v", err)
	}
}

func TestLoadLLMRuntime_CustomConfigPath(t *testing.T) {
	setTestHome(t, t.TempDir())
	// Env endpoints must not mask the config file under test.
	t.Setenv("OCR_LLM_URL", "")
	t.Setenv("OCR_LLM_TOKEN", "")
	t.Setenv("OCR_LLM_MODEL", "")

	cfgPath := filepath.Join(t.TempDir(), "custom.json")
	cfg := `{"llm":{"url":"https://api.example.test/v1","auth_token":"tok","model":"custom-model"},"language":"ja-JP"}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	tpl := loadTestTemplate(t)
	rt, err := loadLLMRuntime(tpl, "", cfgPath, llm.ResolveOptions{})
	if err != nil {
		t.Fatalf("loadLLMRuntime: %v", err)
	}
	if rt.Model != "custom-model" {
		t.Errorf("endpoint model = %q, want the custom config's model", rt.Model)
	}
	if rt.AppCfg == nil || rt.AppCfg.Language != "ja-JP" {
		t.Errorf("app config language = %+v, want the custom config's language", rt.AppCfg)
	}
}
