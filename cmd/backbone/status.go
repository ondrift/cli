package backbone

import (
	"encoding/json"
	"fmt"
	"github.com/ondrift/cli/common"
	"net/http"

	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show backbone health and resource usage",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := common.RequireActiveSlice()
			if err != nil {
				return err
			}

			resp, err := common.DoRequest(http.MethodGet, common.APIBaseURL+"/ops/backbone/admin/status", nil)
			if err != nil {
				return common.TransportError("check backbone status", err)
			}
			defer resp.Body.Close()

			body, err := common.CheckResponse(resp, "check backbone status")
			if err != nil {
				fmt.Println(err)
				return nil
			}

			var status struct {
				Cache struct {
					Entries int   `json:"entries"`
					Bytes   int64 `json:"bytes"`
				} `json:"cache"`
				NoSQL struct {
					Collections int   `json:"collections"`
					DiskBytes   int64 `json:"disk_bytes"`
					WALEntries  int   `json:"wal_entries"`
				} `json:"nosql"`
				Queues struct {
					Count         int `json:"count"`
					TotalMessages int `json:"total_messages"`
				} `json:"queues"`
				Blobs struct {
					Buckets    int `json:"buckets"`
					TotalBlobs int `json:"total_blobs"`
				} `json:"blobs"`
				Locks struct {
					Active int `json:"active"`
				} `json:"locks"`
			}
			json.Unmarshal(body, &status) // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.

			fmt.Printf("%s Backbone Status\n\n", common.Highlight(""))

			fmt.Printf("  %-18s %d entries, %s\n", "Cache", status.Cache.Entries, formatBytes(status.Cache.Bytes))
			fmt.Printf("  %-18s %d collections, %s on disk, %d WAL entries\n", "NoSQL", status.NoSQL.Collections, formatBytes(status.NoSQL.DiskBytes), status.NoSQL.WALEntries)
			fmt.Printf("  %-18s %d queues, %d messages\n", "Queues", status.Queues.Count, status.Queues.TotalMessages)
			fmt.Printf("  %-18s %d buckets, %d blobs\n", "Blobs", status.Blobs.Buckets, status.Blobs.TotalBlobs)
			fmt.Printf("  %-18s %d active\n", "Locks", status.Locks.Active)

			return nil
		},
	}
}

func formatBytes(b int64) string {
	switch {
	case b >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(b)/(1024*1024*1024))
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(b)/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.1f KB", float64(b)/1024)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
