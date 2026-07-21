package backbone

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

func queueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "queue",
		Short:   "Push and pop messages from named queues in your slice",
		Example: "  drift backbone queue push jobs '{\"task\":\"build\",\"ref\":\"main\"}'\n  drift backbone queue pop jobs\n  drift backbone queue peek jobs\n  drift backbone queue len jobs\n  drift backbone queue drop jobs",
	}
	cmd.AddCommand(queuePushCmd(), queuePopCmd(), queuePeekCmd(), queueLenCmd(), queueDropCmd())
	return cmd
}

func queuePushCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "push <name> <json>",
		Short:   "Push a JSON message onto a queue",
		Example: "  drift backbone queue push jobs '{\"task\":\"build\",\"ref\":\"main\"}'\n  drift backbone queue push emails '{\"to\":\"alice@example.com\",\"subject\":\"Hello\"}'",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, rawJSON := args[0], args[1]

			var body map[string]any
			if err := json.Unmarshal([]byte(rawJSON), &body); err != nil {
				e := fmt.Errorf("Couldn't push message: that doesn't look like valid JSON — %v", err)
				fmt.Println(e)
				return e
			}

			payload, _ := json.Marshal(map[string]any{"queue": name, "body": body})
			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/backbone/queue/push",
				bytes.NewBuffer(payload),
			)
			if err != nil {
				e := common.TransportError("push message", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "push message")
			if err != nil {
				fmt.Println(err)
				return err
			}

			var result map[string]string
			if err := json.Unmarshal(b, &result); err == nil {
				fmt.Printf("Message pushed (id: %s)\n", result["id"])
			} else {
				fmt.Println("Message pushed")
			}
			return nil
		},
	}
}

func queuePopCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "pop <name>",
		Short:   "Pop the next message from a queue",
		Example: "  drift backbone queue pop jobs",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			url := fmt.Sprintf("%s/ops/backbone/queue/pop?queue=%s", common.APIBaseURL, url.QueryEscape(name))
			resp, err := common.DoJSONRequest(http.MethodPost, url, nil)
			if err != nil {
				e := common.TransportError("pop message", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusNoContent {
				fmt.Println("Queue is empty.")
				return nil
			}

			b, err := common.CheckResponse(resp, "pop message")
			if err != nil {
				fmt.Println(err)
				return err
			}
			fmt.Println(string(b))
			return nil
		},
	}
}

func queuePeekCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "peek <name>",
		Short:   "Peek at the next message without removing it",
		Example: "  drift backbone queue peek jobs",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			url := fmt.Sprintf("%s/ops/backbone/queue/peek?queue=%s", common.APIBaseURL, url.QueryEscape(name))
			resp, err := common.DoRequest(http.MethodGet, url, nil)
			if err != nil {
				e := common.TransportError("peek queue", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusNoContent {
				fmt.Println("Queue is empty.")
				return nil
			}

			b, err := common.CheckResponse(resp, "peek queue")
			if err != nil {
				fmt.Println(err)
				return err
			}
			fmt.Println(string(b))
			return nil
		},
	}
}

func queueDropCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "drop <name>",
		Short:   "Delete a queue and all its messages",
		Example: "  drift backbone queue drop jobs",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			url := fmt.Sprintf("%s/ops/backbone/queue/drop?queue=%s", common.APIBaseURL, url.QueryEscape(name))
			resp, err := common.DoJSONRequest(http.MethodPost, url, nil)
			if err != nil {
				e := common.TransportError("drop queue", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "drop queue"); err != nil {
				fmt.Println(err)
				return err
			}
			fmt.Printf("Queue %q dropped\n", name)
			return nil
		},
	}
}

func queueLenCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "len <name>",
		Short:   "Print the number of messages in a queue",
		Example: "  drift backbone queue len jobs",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			url := fmt.Sprintf("%s/ops/backbone/queue/len?queue=%s", common.APIBaseURL, url.QueryEscape(name))
			resp, err := common.DoRequest(http.MethodGet, url, nil)
			if err != nil {
				e := common.TransportError("get queue length", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "get queue length")
			if err != nil {
				fmt.Println(err)
				return err
			}

			var result map[string]int
			if err := json.Unmarshal(b, &result); err == nil {
				fmt.Println(result["length"])
			} else {
				fmt.Println(string(b))
			}
			return nil
		},
	}
}
