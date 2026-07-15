package main

import (
	_ "embed"
	"fmt"
	"os"

	account "github.com/ondrift/cli/cmd/account"
	atomic "github.com/ondrift/cli/cmd/atomic"
	backbone "github.com/ondrift/cli/cmd/backbone"
	canvas "github.com/ondrift/cli/cmd/canvas"
	deed "github.com/ondrift/cli/cmd/deed"
	migrate "github.com/ondrift/cli/cmd/migrate"
	portal "github.com/ondrift/cli/cmd/portal"
	project "github.com/ondrift/cli/cmd/project"
	slice "github.com/ondrift/cli/cmd/slice"
	upgrade "github.com/ondrift/cli/cmd/upgrade"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// version is set at build time via:
//
//	go build -ldflags "-X main.version=v1.0.0"
var version = "v1.18.1"

func main() {
	rootCmd := &cobra.Command{
		Use:           "drift",
		Short:         "Drift is a minimalist cloud hosting service.",
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		// Bare `drift` in a terminal launches the full-screen dashboard; in a
		// pipe/CI (non-TTY) it falls back to help so scripts don't hang on a TUI.
		RunE: func(cmd *cobra.Command, args []string) error {
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return cmd.Help()
			}
			return portal.Run(version)
		},
	}

	rootCmd.AddGroup(&cobra.Group{
		ID:    "services",
		Title: "Services:",
	})

	rootCmd.AddGroup(&cobra.Group{
		ID:    "account",
		Title: "Account:",
	})

	rootCmd.AddGroup(&cobra.Group{
		ID:    "project",
		Title: "Project:",
	})

	rootCmd.AddCommand(
		// Project (Driftfile-driven deploy + diff)
		project.GetCmd(),

		// Migrate (read-only lift-off from another cloud, e.g. Azure)
		migrate.GetCmd(),

		// Atomic functions
		atomic.GetCmd(),

		// Canvas (static sites)
		canvas.GetCmd(),

		// Backbone primitives (secrets, queues, blobs)
		backbone.GetCmd(),

		// Deed (identity: KeyAuth, JWT, Vault, Link, Pocket) — status only
		deed.GetCmd(),

		// Slice lifecycle (create, list, use, delete, upgrade)
		slice.GetCmd(),

		// Account (signup, login, usage, upgrade)
		account.GetAccountCmd(),

		// Self-update: reinstall the CLI at latest (or a pinned version)
		upgrade.GetCmd(),

		// Portal — interactive TUI dashboard over your slices/functions/data
		portal.GetCmd(version),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
