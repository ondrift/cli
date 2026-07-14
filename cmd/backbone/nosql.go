package backbone

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

func nosqlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "nosql",
		Short:   "Read and write JSON documents to the Backbone NoSQL store",
		Example: "  drift backbone nosql write --data '{\"key\":\"user-1\",\"name\":\"Alice\"}'\n  drift backbone nosql read --key user-1\n  drift backbone nosql list --collection users --field status --value active\n  drift backbone nosql drop old-logs",
	}
	cmd.AddCommand(nosqlWriteCmd(), nosqlReadCmd(), nosqlListCmd(), nosqlDropCmd())
	return cmd
}

func nosqlWriteCmd() *cobra.Command {
	var collection, data string
	cmd := &cobra.Command{
		Use:     "write",
		Short:   "Write a JSON document to a collection",
		Example: "  drift backbone nosql write --data '{\"key\":\"user-1\",\"name\":\"Alice\"}'\n  drift backbone nosql write --collection users --data '{\"key\":\"user-2\",\"name\":\"Bob\"}'",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if data == "" {
				e := fmt.Errorf("Couldn't write document: --data is required.")
				fmt.Println(e)
				return e
			}

			var body map[string]any
			if err := json.Unmarshal([]byte(data), &body); err != nil {
				e := fmt.Errorf("Couldn't write document: that doesn't look like valid JSON — %v", err)
				fmt.Println(e)
				return e
			}
			if collection != "" {
				body["collection"] = collection
			}

			payload, _ := json.Marshal(body)
			resp, err := common.DoJSONRequest(
				http.MethodPost,
				common.APIBaseURL+"/ops/backbone/write",
				bytes.NewBuffer(payload),
			)
			if err != nil {
				e := common.TransportError("write document", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "write document"); err != nil {
				fmt.Println(err)
				return err
			}

			if collection != "" {
				fmt.Printf("Document written to collection %q\n", collection)
			} else {
				fmt.Println("Document written")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&collection, "collection", "", "Collection name (default: \"default\")")
	cmd.Flags().StringVar(&data, "data", "", "JSON document to write")
	_ = cmd.MarkFlagRequired("data")
	return cmd
}

func nosqlDropCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "drop <collection>",
		Short:   "Delete a NoSQL collection and all its documents",
		Example: "  drift backbone nosql drop old-logs\n  drift backbone nosql drop temp-data",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			url := fmt.Sprintf("%s/ops/backbone/nosql/drop?collection=%s", common.APIBaseURL, url.QueryEscape(name))
			resp, err := common.DoJSONRequest(http.MethodPost, url, nil)
			if err != nil {
				e := common.TransportError("drop collection", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "drop collection"); err != nil {
				fmt.Println(err)
				return err
			}
			fmt.Printf("Collection %q dropped\n", name)
			return nil
		},
	}
}

func nosqlListCmd() *cobra.Command {
	var collection, field, value string
	var limit int
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List all documents in a collection, with optional field filtering",
		Example: "  drift backbone nosql list\n  drift backbone nosql list --collection users\n  drift backbone nosql list --collection users --field status --value active\n  drift backbone nosql list --collection orders --limit 10",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			reqURL := fmt.Sprintf("%s/ops/backbone/nosql/list?collection=%s&limit=%d", common.APIBaseURL, url.QueryEscape(collection), limit)
			if field != "" && value != "" {
				reqURL += "&field=" + url.QueryEscape(field) + "&value=" + url.QueryEscape(value)
			}

			resp, err := common.DoRequest(http.MethodGet, reqURL, nil)
			if err != nil {
				e := common.TransportError("list documents", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "list documents")
			if err != nil {
				fmt.Println(err)
				return err
			}

			// Pretty-print the JSON array.
			var docs []json.RawMessage
			if err := json.Unmarshal(b, &docs); err != nil {
				fmt.Println(string(b))
				return nil
			}

			if len(docs) == 0 {
				fmt.Println("(no documents)")
				return nil
			}

			for _, doc := range docs {
				var pretty bytes.Buffer
				json.Indent(&pretty, doc, "", "  ") // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
				fmt.Println(pretty.String())
			}
			fmt.Printf("\n%d document(s)\n", len(docs))
			return nil
		},
	}
	cmd.Flags().StringVar(&collection, "collection", "default", "Collection name")
	cmd.Flags().StringVar(&field, "field", "", "Filter by field name")
	cmd.Flags().StringVar(&value, "value", "", "Filter by field value (requires --field)")
	cmd.Flags().IntVar(&limit, "limit", 100, "Maximum number of documents to return")
	return cmd
}

func nosqlReadCmd() *cobra.Command {
	var collection, key string
	cmd := &cobra.Command{
		Use:     "read",
		Short:   "Read a document by key from a collection",
		Example: "  drift backbone nosql read --key user-1\n  drift backbone nosql read --collection users --key user-1",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if key == "" {
				e := fmt.Errorf("Couldn't read document: --key is required.\nHint: to browse every document in a collection instead, use 'drift backbone nosql list --collection <name>'.")
				fmt.Println(e)
				return e
			}

			reqURL := fmt.Sprintf("%s/ops/backbone/read?key=%s", common.APIBaseURL, url.QueryEscape(key))
			if collection != "" {
				reqURL += "&collection=" + url.QueryEscape(collection)
			}

			resp, err := common.DoRequest(http.MethodGet, reqURL, nil)
			if err != nil {
				e := common.TransportError("read document", err)
				fmt.Println(e)
				return e
			}
			defer resp.Body.Close()

			b, err := common.CheckResponse(resp, "read document")
			if err != nil {
				fmt.Println(err)
				return err
			}

			fmt.Println(string(b))
			return nil
		},
	}
	cmd.Flags().StringVar(&collection, "collection", "", "Collection name (default: \"default\")")
	cmd.Flags().StringVar(&key, "key", "", "Document key to retrieve")
	// Deliberately not cmd.MarkFlagRequired("key") — that fires cobra's own
	// generic "required flag(s) not set" error before RunE ever runs, which
	// pre-empts the more specific, hint-carrying message below.
	return cmd
}
