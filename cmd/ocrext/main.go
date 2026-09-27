// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

// Command ocr_ext hosts extended ocr commands. Its review command runs the
// standard `ocr review` once per configured model as child processes and
// merges the findings into a single attributed report; it contains no review
// pipeline of its own.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Set via ldflags: -X main.Version=x.y.z -X main.GitCommit=abc123
var (
	Version   = "dev"
	GitCommit = ""
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ocr_ext",
		Short: "OpenCodeReview extensions",
		Long: "Extended ocr commands. `ocr_ext review` runs the standard `ocr review` against the primary model " +
			"plus every configured reviewer in parallel and merges the findings into one report.",
		SilenceUsage: true,
	}
	root.AddCommand(newReviewCmd())
	root.AddCommand(newVersionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("open-code-review %s (ocr_ext", Version)
			if GitCommit != "" {
				fmt.Printf(" %s", GitCommit)
			}
			fmt.Println(")")
		},
	}
}
