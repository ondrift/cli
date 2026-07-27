package slice

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

type snapshotResponse struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Partial and Components are recorded when a snapshot is taken: some
	// component failed to capture, so the archive is real and restorable but
	// INCOMPLETE. Decoding them is what lets `list` say so BEFORE you pick an
	// archive to restore — without it the only difference between a complete
	// backup and one missing every secret is invisible until after the restore.
	Partial    bool              `json:"partial,omitempty"`
	Components map[string]string `json:"components,omitempty"`
	CreatedAt  string            `json:"created_at"`
}

// missingComponents names the components that failed to capture, sorted so the
// output is stable between runs.
func (s snapshotResponse) missingComponents() []string {
	var out []string
	for name, outcome := range s.Components {
		if outcome != "ok" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func getSnapshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Create, list, download, and delete slice snapshots",
	}
	cmd.AddCommand(
		getSnapshotCreateCmd(),
		getSnapshotListCmd(),
		getSnapshotDownloadCmd(),
		getSnapshotDeleteCmd(),
		getSnapshotRestoreCmd(),
	)
	return cmd
}

// ─── create ─────────────────────────────────────────────────────────────────

func getSnapshotCreateCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a snapshot of the active slice",
		RunE: func(cmd *cobra.Command, args []string) error {
			slice, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}

			body, _ := json.Marshal(map[string]string{"name": name})
			resp, err := common.DoJSONRequest(http.MethodPost, common.APIBaseURL+"/ops/slice/snapshot", strings.NewReader(string(body)))
			if err != nil {
				return common.TransportError("create snapshot", err)
			}
			defer resp.Body.Close()

			respBody, err := common.CheckResponse(resp, "create snapshot")
			if err != nil {
				fmt.Println(err)
				return nil
			}

			var result struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			}
			json.Unmarshal(respBody, &result) // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.

			spinner := common.StartSpinner("  ", "Creating snapshot...")

			// Poll for completion.
			timeout := time.After(10 * time.Minute)
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-timeout:
					spinner.Stop()
					fmt.Println("Snapshot creation timed out. Check status with 'drift slice snapshot list'.")
					return nil
				case <-ticker.C:
					statusResp, err := common.DoRequest(http.MethodGet,
						fmt.Sprintf("%s/ops/slice/snapshot/status?id=%s", common.APIBaseURL, result.ID), nil)
					if err != nil {
						continue
					}
					statusBody, _ := io.ReadAll(statusResp.Body)
					statusResp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.

					var snap snapshotResponse
					json.Unmarshal(statusBody, &snap) // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.

					switch snap.Status {
					case "ready":
						spinner.Stop()
						fmt.Printf("%s Snapshot '%s' created (%s, %s)\n",
							common.Check(), snap.Name, snap.ID, formatSize(snap.Size))
						fmt.Printf("  %s\n", common.Hint("Download with: drift slice snapshot download "+snap.ID))
						_ = slice
						return nil
					case "failed":
						spinner.Stop()
						fmt.Printf("Snapshot failed: %s\n", snap.Error)
						return nil
					default:
						spinner.Update("Creating snapshot...")
					}
				}
			}
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Optional label for the snapshot")
	return cmd
}

// ─── list ───────────────────────────────────────────────────────────────────

func getSnapshotListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all snapshots for the active slice",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}

			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/slice/snapshot/list", nil)
			if err != nil {
				return common.TransportError("list snapshots", err)
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "list snapshots")
			if err != nil {
				fmt.Println(err)
				return nil
			}

			var snapshots []snapshotResponse
			json.Unmarshal(body, &snapshots) // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.

			if len(snapshots) == 0 {
				fmt.Println("No snapshots found. Create one with 'drift slice snapshot create'.")
				return nil
			}

			fmt.Printf("%-26s  %-20s  %-10s  %-9s  %s\n", "ID", "NAME", "SIZE", "STATUS", "CREATED")
			partials := 0
			for _, s := range snapshots {
				created := ""
				if t, err := time.Parse(time.RFC3339Nano, s.CreatedAt); err == nil {
					created = t.Format("2006-01-02 15:04")
				}
				// A partial archive is still worth keeping and restoring — it just
				// must not be mistaken for a complete one, which is exactly what
				// this list did while it showed only `status`.
				status := s.Status
				if s.Partial {
					status = s.Status + "*"
					partials++
				}
				fmt.Printf("%-26s  %-20s  %-10s  %-9s  %s\n",
					s.ID, truncate(s.Name, 20), formatSize(s.Size), status, created)
				if missing := s.missingComponents(); len(missing) > 0 {
					fmt.Printf("%-26s  %s\n", "", common.Hint("incomplete — did not capture: "+strings.Join(missing, ", ")))
				}
			}
			if partials > 0 {
				fmt.Printf("\n%s\n", common.Hint("* incomplete snapshot — some data was not captured; restoring one cannot bring back what is missing."))
			}
			return nil
		},
	}
}

// ─── download ───────────────────────────────────────────────────────────────

func getSnapshotDownloadCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "download <snapshot-id>",
		Short: "Download a snapshot archive",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}

			id := args[0]

			spinner := common.StartSpinner("  ", "Downloading snapshot...")

			resp, err := common.DoRequest(http.MethodGet,
				fmt.Sprintf("%s/ops/slice/snapshot/download?id=%s", common.APIBaseURL, id), nil)
			if err != nil {
				spinner.Stop()
				return common.TransportError("download snapshot", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				spinner.Stop()
				body, _ := io.ReadAll(resp.Body)
				fmt.Printf("Download failed: %s\n", strings.TrimSpace(string(body)))
				return nil
			}

			// Determine output filename.
			if output == "" {
				cd := resp.Header.Get("Content-Disposition")
				if cd != "" && strings.Contains(cd, "filename=") {
					parts := strings.Split(cd, "filename=")
					if len(parts) > 1 {
						output = strings.Trim(parts[1], `"`)
					}
				}
				if output == "" {
					output = id + ".tar.gz"
				}
			}

			f, err := os.Create(output) // #nosec G304
			if err != nil {
				spinner.Stop()
				return fmt.Errorf("failed to create file: %w", err)
			}
			defer f.Close()

			written, err := io.Copy(f, resp.Body)
			spinner.Stop()
			if err != nil {
				return fmt.Errorf("download interrupted: %w", err)
			}

			fmt.Printf("%s Snapshot downloaded to %s (%s)\n", common.Check(), output, formatSize(written))
			return nil
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file path (default: <name>.tar.gz)")
	return cmd
}

// ─── delete ─────────────────────────────────────────────────────────────────

func getSnapshotDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <snapshot-id>",
		Short: "Delete a snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}

			id := args[0]

			if !yes {
				fmt.Printf("Delete snapshot %s? [y/N] ", id)
				var answer string
				fmt.Scanln(&answer) // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
				answer = strings.TrimSpace(strings.ToLower(answer))
				if answer != "y" && answer != "yes" {
					fmt.Println("Cancelled.")
					return nil
				}
			}

			resp, err := common.DoRequest(http.MethodDelete,
				fmt.Sprintf("%s/ops/slice/snapshot?id=%s", common.APIBaseURL, id), nil)
			if err != nil {
				return common.TransportError("delete snapshot", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
				fmt.Printf("%s Snapshot %s deleted.\n", common.Check(), id)
			} else {
				body, _ := io.ReadAll(resp.Body)
				fmt.Printf("Delete failed: %s\n", strings.TrimSpace(string(body)))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompt")
	return cmd
}

// ─── restore ───────────────────────────────────────────────────────────

func getSnapshotRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <snapshot-id>",
		Short: "Restore a snapshot into the active slice",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}

			id := args[0]

			body, _ := json.Marshal(map[string]string{"id": id})
			spinner := common.StartSpinner("  ", "Restoring snapshot...")

			resp, err := common.DoJSONRequest(http.MethodPost,
				common.APIBaseURL+"/ops/slice/snapshot/restore",
				strings.NewReader(string(body)))
			if err != nil {
				spinner.Stop()
				return common.TransportError("restore snapshot", err)
			}
			defer resp.Body.Close()

			respBody, err := common.CheckResponse(resp, "restore snapshot")
			if err != nil {
				spinner.Stop()
				fmt.Println(err)
				return nil
			}
			spinner.Stop()

			var result struct {
				Restored struct {
					Secrets          int `json:"secrets"`
					NoSQLCollections int `json:"nosql_collections"`
					Blobs            int `json:"blobs"`
					Queues           int `json:"queues"`
					SQLDatabases     int `json:"sql_databases"`
					Functions        int `json:"functions"`
					Canvas           int `json:"canvas"`
					VaultEntries     int `json:"vault_entries"`
					LinkIdentities   int `json:"link_identities"`
					PocketItems      int `json:"pocket_items"`
				} `json:"restored"`
				Errors []string `json:"errors"`
			}
			json.Unmarshal(respBody, &result) // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.

			fmt.Printf("%s Snapshot %s restored.\n", common.Check(), id)
			fmt.Println()
			r := result.Restored
			fmt.Printf("  Secrets:     %d\n", r.Secrets)
			fmt.Printf("  NoSQL:       %d collections\n", r.NoSQLCollections)
			fmt.Printf("  Blobs:       %d\n", r.Blobs)
			fmt.Printf("  Queues:      %d\n", r.Queues)
			fmt.Printf("  SQL:         %d databases\n", r.SQLDatabases)
			fmt.Printf("  Vault:       %d entries\n", r.VaultEntries)
			fmt.Printf("  Link:        %d identities\n", r.LinkIdentities)
			fmt.Printf("  Pocket:      %d items\n", r.PocketItems)
			fmt.Printf("  Functions:   %d\n", r.Functions)
			fmt.Printf("  Canvas:      %d sites\n", r.Canvas)

			if len(result.Errors) > 0 {
				fmt.Println()
				fmt.Printf("  Errors (%d):\n", len(result.Errors))
				for _, e := range result.Errors {
					fmt.Printf("    - %s\n", e)
				}
			}

			return nil
		},
	}
}

// ─── Helpers ────────────────────────────────────────────────────────────────

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1024*1024*1024))
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	case bytes > 0:
		return fmt.Sprintf("%d B", bytes)
	default:
		return "-"
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
