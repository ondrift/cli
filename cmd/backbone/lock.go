package backbone

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

func lockCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "lock",
		Short:   "Acquire and release distributed locks in your slice",
		Example: "  drift backbone lock acquire deploy-lock worker-1 --ttl 60\n  drift backbone lock renew deploy-lock worker-1 --ttl 60\n  drift backbone lock release deploy-lock worker-1",
	}
	cmd.AddCommand(lockAcquireCmd(), lockReleaseCmd(), lockRenewCmd())
	return cmd
}

func lockAcquireCmd() *cobra.Command {
	var ttl int
	cmd := &cobra.Command{
		Use:     "acquire <name> <owner>",
		Short:   "Acquire a named lock",
		Example: "  drift backbone lock acquire deploy-lock worker-1\n  drift backbone lock acquire db-migration runner-0 --ttl 120",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, owner := args[0], args[1]

			payload, _ := json.Marshal(map[string]any{"name": name, "owner": owner, "ttl": ttl})
			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/backbone/lock/acquire",
				bytes.NewBuffer(payload),
			)
			if err != nil {
				e := common.TransportError("acquire lock", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusConflict {
				e := fmt.Errorf("Lock %q is already held.", name)
				fmt.Println(e)
				return e
			}
			if _, err := common.CheckResponse(resp, "acquire lock"); err != nil {
				fmt.Println(err)
				return err
			}

			fmt.Printf("Lock %q acquired by %q (ttl: %ds)\n", name, owner, ttl)
			return nil
		},
	}
	cmd.Flags().IntVar(&ttl, "ttl", 30, "Lock TTL in seconds")
	return cmd
}

func lockReleaseCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "release <name> <owner>",
		Short:   "Release a named lock",
		Example: "  drift backbone lock release deploy-lock worker-1",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, owner := args[0], args[1]

			payload, _ := json.Marshal(map[string]any{"name": name, "owner": owner})
			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/backbone/lock/release",
				bytes.NewBuffer(payload),
			)
			if err != nil {
				e := common.TransportError("release lock", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "release lock"); err != nil {
				fmt.Println(err)
				return err
			}

			fmt.Printf("Lock %q released\n", name)
			return nil
		},
	}
}

func lockRenewCmd() *cobra.Command {
	var ttl int
	cmd := &cobra.Command{
		Use:     "renew <name> <owner>",
		Short:   "Renew a lock's TTL",
		Example: "  drift backbone lock renew deploy-lock worker-1\n  drift backbone lock renew deploy-lock worker-1 --ttl 120",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, owner := args[0], args[1]

			payload, _ := json.Marshal(map[string]any{"name": name, "owner": owner, "ttl": ttl})
			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/backbone/lock/renew",
				bytes.NewBuffer(payload),
			)
			if err != nil {
				e := common.TransportError("renew lock", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "renew lock"); err != nil {
				fmt.Println(err)
				return err
			}

			fmt.Printf("Lock %q renewed (ttl: %ds)\n", name, ttl)
			return nil
		},
	}
	cmd.Flags().IntVar(&ttl, "ttl", 30, "New TTL in seconds")
	return cmd
}
