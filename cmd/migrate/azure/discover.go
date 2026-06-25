package azure

import "strings"

// MoveClass is how a discovered resource fares against Drift's primitives.
type MoveClass int

const (
	// Movable — a Drift primitive covers it (the tool can scaffold it).
	Movable MoveClass = iota
	// Refused — no Drift primitive; it stays on Azure, by design and on the
	// record. Never counted as a saving.
	Refused
	// Verify — probably movable, but a fact (usually runtime) couldn't be
	// determined from inventory alone. Confirmed at `snapshot`/`transform`.
	Verify
)

func (m MoveClass) String() string {
	switch m {
	case Movable:
		return "movable"
	case Refused:
		return "refused"
	default:
		return "verify"
	}
}

// FunctionApp is one Azure Function App, classified by its runtime.
type FunctionApp struct {
	Name    string
	Runtime string // python | node | dotnet | java | powershell | unknown
	Class   MoveClass
	Reason  string // why Refused/Verify (empty when Movable)
}

// NamedResource is a discovered resource we only need to count for the estimate.
type NamedResource struct {
	Name string
	Type string
}

// Inventory is the normalized result of discovering one resource group.
type Inventory struct {
	Subscription  string
	ResourceGroup string
	FunctionApps  []FunctionApp
	Cosmos        []NamedResource
	Storage       []NamedResource
	StaticSites   []NamedResource
	Other         []NamedResource
}

// --- az JSON shapes (only the fields we read) ------------------------------

type azFunctionApp struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	SiteConfig struct {
		LinuxFxVersion string `json:"linuxFxVersion"`
	} `json:"siteConfig"`
}

type azResource struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Kind string `json:"kind"`
}

// detectRuntime classifies a Function App's worker runtime from the fields
// `az functionapp list` returns. Unknown is honest, not optimistic — it
// becomes a Verify, not a silent Movable.
func detectRuntime(linuxFxVersion, kind string) string {
	s := strings.ToLower(linuxFxVersion + " " + kind)
	switch {
	case strings.Contains(s, "python"):
		return "python"
	case strings.Contains(s, "node"):
		return "node"
	case strings.Contains(s, "dotnet"), strings.Contains(s, ".net"):
		return "dotnet"
	case strings.Contains(s, "java"):
		return "java"
	case strings.Contains(s, "powershell"):
		return "powershell"
	default:
		return "unknown"
	}
}

// classify maps a runtime to a MoveClass + a human reason. The MVP scaffolds
// Python and Node; .NET / Java / PowerShell have no Drift SDK and are refused
// on the record; unknown needs a look at the code.
func classify(runtime string) (MoveClass, string) {
	switch runtime {
	case "python", "node":
		return Movable, ""
	case "dotnet":
		return Refused, "no Drift SDK for .NET — stays on Azure"
	case "java":
		return Refused, "no Drift SDK for Java — stays on Azure"
	case "powershell":
		return Refused, "no Drift SDK for PowerShell — stays on Azure"
	default:
		return Verify, "runtime not determined from inventory — verify at snapshot"
	}
}

// discover inventories one resource group: Function Apps (with runtime), and a
// type-categorized pass over everything else. Two read-only az calls.
func discover(c azClient, sub, rg string) (Inventory, error) {
	inv := Inventory{Subscription: sub, ResourceGroup: rg}

	var fas []azFunctionApp
	if err := c.runJSON([]string{"functionapp", "list", "-g", rg}, &fas); err != nil {
		return inv, err
	}
	for _, fa := range fas {
		rt := detectRuntime(fa.SiteConfig.LinuxFxVersion, fa.Kind)
		class, reason := classify(rt)
		inv.FunctionApps = append(inv.FunctionApps, FunctionApp{
			Name: fa.Name, Runtime: rt, Class: class, Reason: reason,
		})
	}

	var res []azResource
	if err := c.runJSON([]string{"resource", "list", "-g", rg}, &res); err != nil {
		return inv, err
	}
	for _, r := range res {
		nr := NamedResource{Name: r.Name, Type: r.Type}
		switch {
		case strings.EqualFold(r.Type, "Microsoft.DocumentDB/databaseAccounts"):
			inv.Cosmos = append(inv.Cosmos, nr)
		case strings.EqualFold(r.Type, "Microsoft.Storage/storageAccounts"):
			inv.Storage = append(inv.Storage, nr)
		case strings.EqualFold(r.Type, "Microsoft.Web/staticSites"):
			inv.StaticSites = append(inv.StaticSites, nr)
		case strings.EqualFold(r.Type, "Microsoft.Web/sites"):
			// Function Apps already enumerated above with richer detail; skip.
		default:
			inv.Other = append(inv.Other, nr)
		}
	}
	return inv, nil
}
