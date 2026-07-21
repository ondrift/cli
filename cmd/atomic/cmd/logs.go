package atomic_cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

type logEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Line      string    `json:"line"`
}

func fetchLogs(function string) ([]logEntry, error) {
	resp, err := common.DoRequest(
		http.MethodGet,
		common.APIBaseURL+"/ops/atomic/logs?function="+function,
		nil,
	)
	if err != nil {
		return nil, common.TransportError("fetch logs", err)
	}
	defer resp.Body.Close()

	b, err := common.CheckResponse(resp, "fetch logs")
	if err != nil {
		return nil, err
	}

	var entries []logEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("Couldn't fetch logs: the API response didn't look right (%w)", err)
	}
	return entries, nil
}

func printLog(e logEntry) {
	fmt.Printf("%s  %s\n", e.Timestamp.Format("2006-01-02 15:04:05.000"), e.Line)
}

func Logs() *cobra.Command {
	var tail int
	var follow bool

	cmd := &cobra.Command{
		Use:     "logs <function-name>",
		Short:   "Fetch recent logs for a deployed function",
		Example: "  drift atomic logs send-email\n  drift atomic logs send-email --tail 50\n  drift atomic logs send-email -f\n  drift atomic logs purge send-email",
		GroupID: "operations",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			function := args[0]

			entries, err := fetchLogs(function)
			if err != nil {
				return err
			}

			if !follow && len(entries) == 0 {
				fmt.Printf("No logs found for %q\n", function)
				return nil
			}

			if tail > 0 && tail < len(entries) {
				entries = entries[len(entries)-tail:]
			}

			for _, e := range entries {
				printLog(e)
			}

			if !follow {
				return nil
			}

			// --follow: poll every 1s, print only entries newer than the
			// last printed one. We watch the timestamp of the last entry
			// and skip anything we've already seen. The platform stores
			// logs in-memory and may purge between polls; if the count
			// shrinks we accept the new tail without re-printing the
			// overlap.
			var lastTS time.Time
			if len(entries) > 0 {
				lastTS = entries[len(entries)-1].Timestamp
			}
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				<-ticker.C
				next, err := fetchLogs(function)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "logs fetch error (will retry): %v\n", err)
					continue
				}
				for _, e := range next {
					if e.Timestamp.After(lastTS) {
						printLog(e)
						lastTS = e.Timestamp
					}
				}
			}
		},
	}

	cmd.Flags().IntVarP(&tail, "tail", "n", 0, "Show only the N most recent lines (default: all)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Stream new log lines as they appear (poll every 1s, Ctrl-C to stop)")
	cmd.AddCommand(logsPurgeCmd())
	return cmd
}

func logsPurgeCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "purge <function-name>",
		Short:   "Clear in-memory log entries for a function",
		Example: "  drift atomic logs purge send-email",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			function := args[0]

			resp, err := common.DoRequest(
				http.MethodDelete,
				common.APIBaseURL+"/ops/atomic/logs?function="+function,
				nil,
			)
			if err != nil {
				return common.TransportError("purge logs", err)
			}
			defer resp.Body.Close()

			_, err = common.CheckResponse(resp, "purge logs")
			if err != nil {
				fmt.Println(err)
				return nil
			}

			fmt.Printf("Logs purged for %s.\n", common.Highlight(function))
			return nil
		},
	}
}
