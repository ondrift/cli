package backbone

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

func blobCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "blob",
		Short:   "Store and retrieve binary blobs in your slice",
		Example: "  drift backbone blob put assets logo.png ./logo.png\n  drift backbone blob get assets logo.png > logo.png\n  drift backbone blob list assets\n  drift backbone blob delete assets logo.png",
	}
	cmd.AddCommand(blobPutCmd(), blobGetCmd(), blobListCmd(), blobDeleteCmd())
	return cmd
}

func blobPutCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "put <bucket> <key> <file>",
		Short:   "Upload a file to a blob bucket",
		Example: "  drift backbone blob put assets logo.png ./logo.png\n  drift backbone blob put uploads report.pdf ./report.pdf",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			bucket, key, file := args[0], args[1], args[2]

			// #nosec G304 -- path is constructed from a validated function/element/key name (regex-pinned ^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$) plus a base directory under the slice's PVC; not user-controlled.

			f, err := os.Open(file)
			if err != nil {
				e := fmt.Errorf("Couldn't put blob: failed to open %s (%v)", file, err)
				fmt.Println(e)
				return e
			}
			defer f.Close()

			url := fmt.Sprintf("%s/ops/backbone/blob/put?bucket=%s&key=%s", common.APIBaseURL, url.QueryEscape(bucket), url.QueryEscape(key))
			resp, err := common.DoRequestWithContentType(http.MethodPost, url, "application/octet-stream", f)
			if err != nil {
				e := common.TransportError("put blob", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "put blob"); err != nil {
				fmt.Println(err)
				return err
			}

			fmt.Printf("Blob stored at %s/%s\n", bucket, key)
			return nil
		},
	}
}

func blobGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <bucket> <key>",
		Short:   "Download a blob and write it to stdout",
		Example: "  drift backbone blob get assets logo.png > logo.png\n  drift backbone blob get uploads report.pdf > report.pdf",
		Args:    cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			bucket, key := args[0], args[1]

			url := fmt.Sprintf("%s/ops/backbone/blob/get?bucket=%s&key=%s", common.APIBaseURL, url.QueryEscape(bucket), url.QueryEscape(key))
			resp, err := common.DoRequest(http.MethodGet, url, nil)
			if err != nil {
				fmt.Fprintln(os.Stderr, common.TransportError("get blob", err))
				os.Exit(1)
			}
			defer resp.Body.Close()

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				if _, err := common.CheckResponse(resp, "get blob"); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
			}

			if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
				fmt.Fprintf(os.Stderr, "Couldn't get blob: error while streaming the body (%v)\n", err)
				os.Exit(1)
			}
		},
	}
}

func blobListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list <bucket>",
		Short:   "List keys in a blob bucket",
		Example: "  drift backbone blob list assets",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			bucket := args[0]

			url := fmt.Sprintf("%s/ops/backbone/blob/list?bucket=%s", common.APIBaseURL, url.QueryEscape(bucket))
			resp, err := common.DoRequest(http.MethodGet, url, nil)
			if err != nil {
				e := common.TransportError("list blobs", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "list blobs")
			if err != nil {
				fmt.Println(err)
				return err
			}

			var keys []string
			if err := json.Unmarshal(b, &keys); err != nil || len(keys) == 0 {
				fmt.Println("No blobs in bucket.")
				return nil
			}
			for _, k := range keys {
				fmt.Println(k)
			}
			return nil
		},
	}
}

func blobDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <bucket> <key>",
		Short:   "Delete a blob",
		Example: "  drift backbone blob delete assets logo.png",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			bucket, key := args[0], args[1]

			url := fmt.Sprintf("%s/ops/backbone/blob/delete?bucket=%s&key=%s", common.APIBaseURL, url.QueryEscape(bucket), url.QueryEscape(key))
			resp, err := common.DoJSONRequest(http.MethodPost, url, nil)
			if err != nil {
				e := common.TransportError("delete blob", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "delete blob"); err != nil {
				fmt.Println(err)
				return err
			}

			fmt.Printf("Blob %s/%s deleted\n", bucket, key)
			return nil
		},
	}
}
