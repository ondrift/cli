package slate_cmd

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

func Deploy() *cobra.Command {
	var route string

	cmd := &cobra.Command{
		Use:   "deploy [directory]",
		Short: "Deploy a static site from a directory",
		Example: "  drift canvas deploy ./my-site\n" +
			"  drift canvas deploy ./dist --route /admin",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			folder := args[0]
			r := canonicalRoute(route)
			slug := slugifyRoute(r)
			fmt.Printf("Deploying canvas site from %s → %s\n", folder, r)

			zipData, err := common.ZipFolder(folder)
			if err != nil {
				fmt.Printf("Couldn't deploy canvas site: failed to zip folder (%v)\n", err)
				return
			}

			q := url.Values{}
			q.Set("site", slug)
			q.Set("route", r)
			resp, err := common.DoRequestWithHeaders(
				http.MethodPost,
				common.APIBaseURL+"/ops/canvas?"+q.Encode(),
				zipData,
				map[string]string{
					"Content-Type": "application/zip",
				},
			)
			if err != nil {
				fmt.Println(common.TransportError("deploy canvas site", err))
				return
			}
			defer resp.Body.Close()

			if _, err := common.CheckResponse(resp, "deploy canvas site"); err != nil {
				fmt.Println(err)
				return
			}

			fmt.Println("Canvas site deployed.")
		},
	}
	cmd.Flags().StringVar(&route, "route", "/", "URL prefix to mount the site at (e.g. /admin)")
	return cmd
}

func canonicalRoute(route string) string {
	if route == "" {
		return "/"
	}
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	route = strings.TrimRight(route, "/")
	if route == "" {
		return "/"
	}
	return route
}

func slugifyRoute(route string) string {
	r := canonicalRoute(route)
	if r == "/" {
		return "default"
	}
	return strings.ReplaceAll(strings.TrimPrefix(r, "/"), "/", "-")
}
