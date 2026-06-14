package main

import (
	_ "embed"
	"fmt"
	"os"

	account "github.com/ondrift/cli/cmd/account"
	atomic "github.com/ondrift/cli/cmd/atomic"
	backbone "github.com/ondrift/cli/cmd/backbone"
	canvas "github.com/ondrift/cli/cmd/canvas"
	project "github.com/ondrift/cli/cmd/project"
	slice "github.com/ondrift/cli/cmd/slice"

	"github.com/spf13/cobra"
)

// version is set at build time via:
//
//	go build -ldflags "-X main.version=v1.0.0"
var version = "v1.4.0"

func main() {
	rootCmd := &cobra.Command{
		Use:           "drift",
		Short:         "Drift is a minimalist cloud hosting service.",
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
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

		// Deployment planning
		account.GetPlanCmd(),

		// Atomic functions
		atomic.GetCmd(),

		// Canvas (static sites)
		canvas.GetCmd(),

		// Backbone primitives (secrets, queues, blobs)
		backbone.GetCmd(),

		// Slice lifecycle (create, list, use, delete, upgrade)
		slice.GetCmd(),

		// Account (signup, login, usage, upgrade)
		account.GetAccountCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
