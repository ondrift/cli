package atomic_cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func Element() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "element",
		Short:   "Manage Atomic elements (virtual API groups)",
		Example: "  drift atomic element list",
		GroupID: "operations",
	}
	cmd.AddCommand(elementList())
	return cmd
}

func elementList() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all elements and the functions they contain",
		Example: "  drift atomic element list",
		Args:    cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			deployed, err := fetchDeployedFunctions()
			if err != nil {
				fmt.Println(err)
				return
			}

			byElement := make(map[string][]atomicRecord)
			for _, r := range deployed {
				if r.Element != "" {
					byElement[r.Element] = append(byElement[r.Element], r)
				}
			}

			if len(byElement) == 0 {
				fmt.Println("No elements defined. Deploy with --element <name> to create one.")
				return
			}

			names := make([]string, 0, len(byElement))
			for name := range byElement {
				names = append(names, name)
			}
			sort.Strings(names)

			for _, name := range names {
				fns := byElement[name]
				fmt.Printf("%s  (%d function", name, len(fns))
				if len(fns) != 1 {
					fmt.Print("s")
				}
				fmt.Println(")")

				for _, r := range fns {
					route := fmt.Sprintf("/api/%s/%s", name, r.FunctionName)
					methods := r.Method
					if methods == "" {
						methods = "?"
					}
					fmt.Printf("  %-8s  %-32s  %s\n", strings.ToUpper(methods), route, r.Id)
				}
				fmt.Println()
			}
		},
	}
}
