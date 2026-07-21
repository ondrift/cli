package backbone

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

func cacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "cache",
		Short:   "In-memory key-value cache in your slice",
		Example: "  drift backbone cache set session-token abc123 --ttl 3600\n  drift backbone cache get session-token\n  drift backbone cache exists session-token\n  drift backbone cache del session-token",
	}
	cmd.AddCommand(cacheSetCmd(), cacheGetCmd(), cacheDelCmd(), cacheExistsCmd())
	return cmd
}

func cacheSetCmd() *cobra.Command {
	var ttl int
	cmd := &cobra.Command{
		Use:     "set <key> <value>",
		Short:   "Set a cache key",
		Example: "  drift backbone cache set session-token abc123\n  drift backbone cache set rate-limit 100 --ttl 60",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]

			payload, _ := json.Marshal(map[string]any{"key": key, "value": value, "ttl": ttl})
			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/backbone/cache/set",
				bytes.NewBuffer(payload),
			)
			if err != nil {
				e := common.TransportError("set cache key", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "set cache key"); err != nil {
				fmt.Println(err)
				return err
			}

			if ttl > 0 {
				fmt.Printf("%q set (ttl: %ds)\n", key, ttl)
			} else {
				fmt.Printf("%q set (no expiry)\n", key)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&ttl, "ttl", 0, "TTL in seconds (0 = no expiry)")
	return cmd
}

func cacheGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <key>",
		Short:   "Get a cache value",
		Example: "  drift backbone cache get session-token",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]

			resp, err := common.DoRequest(
				http.MethodGet,
				common.APIBaseURL+"/ops/backbone/cache/get?key="+key,
				nil,
			)
			if err != nil {
				e := common.TransportError("get cache key", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusNotFound {
				e := fmt.Errorf("Key %q not found.", key)
				fmt.Println(e)
				return e
			}
			b, err := common.CheckResponse(resp, "get cache key")
			if err != nil {
				fmt.Println(err)
				return err
			}
			fmt.Println(string(b))
			return nil
		},
	}
}

func cacheDelCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "del <key>",
		Short:   "Delete a cache key",
		Example: "  drift backbone cache del session-token",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]

			payload, _ := json.Marshal(map[string]string{"key": key})
			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/backbone/cache/del",
				bytes.NewBuffer(payload),
			)
			if err != nil {
				e := common.TransportError("delete cache key", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusNotFound {
				fmt.Printf("Key %q not found.\n", key)
				return nil
			}
			if _, err := common.CheckResponse(resp, "delete cache key"); err != nil {
				fmt.Println(err)
				return err
			}

			fmt.Printf("%q deleted\n", key)
			return nil
		},
	}
}

func cacheExistsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "exists <key>",
		Short:   "Check whether a cache key exists",
		Example: "  drift backbone cache exists session-token",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]

			resp, err := common.DoRequest(
				http.MethodGet,
				common.APIBaseURL+"/ops/backbone/cache/exists?key="+key,
				nil,
			)
			if err != nil {
				e := common.TransportError("check cache key", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "check cache key")
			if err != nil {
				fmt.Println(err)
				return err
			}
			var result struct {
				Exists bool `json:"exists"`
			}
			if err := json.Unmarshal(b, &result); err != nil {
				e := fmt.Errorf("Couldn't check cache key: the API response didn't look right (%s)", string(b))
				fmt.Println(e)
				return e
			}

			if result.Exists {
				fmt.Printf("%q exists\n", key)
			} else {
				fmt.Printf("%q does not exist\n", key)
			}
			return nil
		},
	}
}
