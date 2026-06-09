package backbone

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

func secretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "secret",
		Short:   "Manage encrypted secrets in your slice",
		Example: "  drift backbone secret set API_KEY=sk-abc123\n  drift backbone secret get API_KEY\n  drift backbone secret list\n  drift backbone secret delete API_KEY",
	}
	cmd.AddCommand(secretSetCmd(), secretGetCmd(), secretListCmd(), secretDeleteCmd())
	return cmd
}

func secretSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "set KEY=VALUE",
		Short:   "Store an encrypted secret",
		Example: "  drift backbone secret set API_KEY=sk-abc123\n  drift backbone secret set DB_PASSWORD=hunter2",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parts := strings.SplitN(args[0], "=", 2)
			if len(parts) != 2 || parts[0] == "" {
				e := fmt.Errorf("Couldn't store secret: argument must be in KEY=VALUE format.")
				fmt.Println(e)
				return e
			}
			name, value := parts[0], parts[1]

			body, _ := json.Marshal(map[string]string{"name": name, "value": value})
			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/backbone/secret/set",
				bytes.NewBuffer(body),
			)
			if err != nil {
				e := common.TransportError("store secret", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "store secret"); err != nil {
				fmt.Println(err)
				return err
			}

			fmt.Printf("Secret %q stored\n", name)
			return nil
		},
	}
}

func secretGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get KEY",
		Short:   "Retrieve the value of a secret",
		Example: "  drift backbone secret get API_KEY",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := common.DoRequest(
				http.MethodGet,
				common.APIBaseURL+"/ops/backbone/secret/get?name="+args[0],
				nil,
			)
			if err != nil {
				e := common.TransportError("get secret", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "get secret")
			if err != nil {
				fmt.Println(err)
				return err
			}

			fmt.Println(string(b))
			return nil
		},
	}
}

func secretListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List secret names",
		Example: "  drift backbone secret list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := common.DoRequest(
				http.MethodGet,
				common.APIBaseURL+"/ops/backbone/secret/list",
				nil,
			)
			if err != nil {
				e := common.TransportError("list secrets", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "list secrets")
			if err != nil {
				fmt.Println(err)
				return err
			}

			var names []string
			if err := json.Unmarshal(b, &names); err != nil || len(names) == 0 {
				fmt.Println("No secrets stored.")
				return nil
			}
			for _, n := range names {
				fmt.Println(n)
			}
			return nil
		},
	}
}

func secretDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete KEY",
		Short:   "Delete a secret",
		Example: "  drift backbone secret delete API_KEY",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, _ := json.Marshal(map[string]string{"name": args[0]})
			resp, err := common.DoJSONRequest(
				http.MethodDelete,
				common.APIBaseURL+"/ops/backbone/secret/delete",
				bytes.NewBuffer(body),
			)
			if err != nil {
				e := common.TransportError("delete secret", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "delete secret"); err != nil {
				fmt.Println(err)
				return err
			}

			fmt.Printf("Secret %q deleted\n", args[0])
			return nil
		},
	}
}
