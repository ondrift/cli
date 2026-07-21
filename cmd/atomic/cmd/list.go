package atomic_cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

type atomicRecord struct {
	Id           string `json:"_id"`
	Name         string `json:"name"`
	FunctionName string `json:"function_name"`
	Method       string `json:"method"`
	Element      string `json:"element"`
	Language     string `json:"language"`
	CreatedAt    string `json:"created_at"`
}

func fetchDeployedFunctions() ([]atomicRecord, error) {
	resp, err := common.DoRequest(
		http.MethodGet,
		common.APIBaseURL+"/ops/atomic/list",
		nil,
	)
	if err != nil {
		return nil, common.TransportError("list atomic functions", err)
	}
	defer resp.Body.Close()

	b, err := common.CheckResponse(resp, "list atomic functions")
	if err != nil {
		return nil, err
	}

	var records []atomicRecord
	if err := json.Unmarshal(b, &records); err != nil {
		return nil, fmt.Errorf("Couldn't list atomic functions: the API response didn't look right (%w)", err)
	}

	// Filter to only deployed (pre-warmed slots have no function name).
	var deployed []atomicRecord
	for _, r := range records {
		if r.FunctionName != "" {
			deployed = append(deployed, r)
		}
	}
	return deployed, nil
}

func langOrDefault(r atomicRecord) string {
	switch r.Language {
	case "", "native":
		return "go"
	case "python":
		return "python"
	case "node":
		return "node"
	default:
		return r.Language
	}
}

func printFlatTable(records []atomicRecord) {
	fmt.Printf("%-12s  %-8s  %-8s  %-24s  %s\n", "ID", "LANG", "METHOD", "ROUTE", "Deployed At")
	fmt.Printf("%-12s  %-8s  %-8s  %-24s  %s\n", "------------", "--------", "--------", "------------------------", "-------------------")
	for _, r := range records {
		// Org-only routing: the route is the bare function name; the element is
		// a grouping concept (shown in the grouped view), never a path segment.
		fmt.Printf("%-12s  %-8s  %-8s  %-24s  %s\n", r.Id, langOrDefault(r), r.Method, r.FunctionName, r.CreatedAt)
	}
}

func List() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all deployed atomic functions",
		Example: "  drift atomic list",
		GroupID: "operations",
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			deployed, err := fetchDeployedFunctions()
			if err != nil {
				fmt.Println(err)
				return
			}
			if len(deployed) == 0 {
				fmt.Println("No functions deployed.")
				return
			}

			// Check if any function belongs to an element.
			hasElements := false
			for _, r := range deployed {
				if r.Element != "" {
					hasElements = true
					break
				}
			}

			if !hasElements {
				printFlatTable(deployed)
				return
			}

			// Group by element; collect ungrouped separately.
			byElement := make(map[string][]atomicRecord)
			var ungrouped []atomicRecord
			for _, r := range deployed {
				if r.Element == "" {
					ungrouped = append(ungrouped, r)
				} else {
					byElement[r.Element] = append(byElement[r.Element], r)
				}
			}

			elementNames := make([]string, 0, len(byElement))
			for name := range byElement {
				elementNames = append(elementNames, name)
			}
			sort.Strings(elementNames)

			header := fmt.Sprintf("%-12s  %-8s  %-8s  %s", "ID", "LANG", "METHOD", "FUNCTION")
			rule := fmt.Sprintf("%-12s  %-8s  %-8s  %s", "------------", "--------", "--------", "--------")

			for _, name := range elementNames {
				fmt.Printf("\n  element: %s\n", name)
				fmt.Printf("  %s\n", header)
				fmt.Printf("  %s\n", rule)
				for _, r := range byElement[name] {
					fmt.Printf("  %-12s  %-8s  %-8s  %s\n", r.Id, langOrDefault(r), r.Method, r.FunctionName)
				}
			}

			if len(ungrouped) > 0 {
				fmt.Printf("\n  (no element)\n")
				fmt.Printf("  %s\n", header)
				fmt.Printf("  %s\n", rule)
				for _, r := range ungrouped {
					fmt.Printf("  %-12s  %-8s  %-8s  %s\n", r.Id, langOrDefault(r), r.Method, r.FunctionName)
				}
			}
			fmt.Println()
		},
	}
}
