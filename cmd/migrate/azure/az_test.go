package azure

import "testing"

func TestActionVerb(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"resource", "list", "-g", "rg"}, "list"},
		{[]string{"consumption", "usage", "list", "-o", "json"}, "list"},
		{[]string{"account", "show"}, "show"},
		{[]string{"functionapp", "create", "-g", "rg"}, "create"},
		{[]string{"group", "delete", "--name", "x"}, "delete"},
	}
	for _, c := range cases {
		if got := actionVerb(c.args); got != c.want {
			t.Errorf("actionVerb(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestGuardAllowsReadOnly(t *testing.T) {
	ok := [][]string{
		{"resource", "list", "-g", "rg"},
		{"functionapp", "list", "-g", "rg"},
		{"consumption", "usage", "list"},
		{"account", "show"},
		{"version"},
	}
	for _, args := range ok {
		if err := guard(args); err != nil {
			t.Errorf("guard refused read-only command az %v: %v", args, err)
		}
	}
}

func TestGuardRefusesMutating(t *testing.T) {
	bad := [][]string{
		{"functionapp", "create", "-g", "rg"},
		{"resource", "delete", "--ids", "x"},
		{"group", "update", "--name", "x"},
		{"storage", "account", "create"},
		{"functionapp", "deployment", "source", "config"},
	}
	for _, args := range bad {
		if err := guard(args); err == nil {
			t.Errorf("guard ALLOWED a non-read-only command az %v — read-only is a safety contract", args)
		}
	}
}
