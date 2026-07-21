package backbone

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

// liveSQL mirrors the slice's `/ops/backbone/sql/admin/list` shape — the
// same struct the project deploy reconcile reads.
type liveSQL struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
}

func sqlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sql",
		Short:   "Inspect and manage your slice's SQL databases",
		Example: "  drift backbone sql list\n  drift backbone sql drop ledger",
	}
	cmd.AddCommand(sqlListCmd(), sqlDropCmd())
	return cmd
}

func sqlListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List the SQL databases on the slice and their on-disk size",
		Example: "  drift backbone sql list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := common.DoRequest(http.MethodGet,
				common.APIBaseURL+"/ops/backbone/sql/admin/list", nil)
			if err != nil {
				e := common.TransportError("list sql databases", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "list sql databases")
			if err != nil {
				fmt.Println(err)
				return err
			}

			var dbs []liveSQL
			if err := json.Unmarshal(b, &dbs); err != nil {
				fmt.Println(string(b))
				return nil
			}
			if len(dbs) == 0 {
				fmt.Println("(no SQL databases)")
				return nil
			}
			for _, d := range dbs {
				fmt.Printf("  %-32s %s\n", d.Name, humanBytes(d.SizeBytes))
			}
			fmt.Printf("\n%d database(s)\n", len(dbs))
			return nil
		},
	}
}

func sqlDropCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "drop <name>",
		Short:   "Delete a SQL database and all its tables and rows",
		Example: "  drift backbone sql drop ledger\n  drift backbone sql drop temp-import",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			payload, _ := json.Marshal(map[string]string{"db": name})
			resp, err := common.DoJSONRequest(http.MethodPost,
				common.APIBaseURL+"/ops/backbone/sql/admin/drop",
				bytes.NewBuffer(payload))
			if err != nil {
				e := common.TransportError("drop sql database", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "drop sql database"); err != nil {
				fmt.Println(err)
				return err
			}
			fmt.Printf("Database %q dropped\n", name)
			return nil
		},
	}
}

// humanBytes renders a byte count as a short human-readable size.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
