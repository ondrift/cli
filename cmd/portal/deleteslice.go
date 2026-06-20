package portal

// Delete-slice flow (D in the sidebar): a roomy, in-pane confirmation that
// mirrors `drift slice delete <name>` — the right pane turns red and titled
// "Deleting <name>", spells out exactly what's destroyed, and makes you type the
// slice name to confirm (Esc cancels). The actual DELETE lives in fetch.go.

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/ondrift/cli/common"
)

type deleteSlice struct{ name string }

// startDeleteSlice arms the delete confirmation for name: it sets the red
// "Deleting" mode and opens the type-the-name-to-confirm prompt.
func (m *model) startDeleteSlice(name string) {
	m.deleting = &deleteSlice{name: name}
	m.focus = focusMain
	m.input = &inputPrompt{
		label: "type \"" + name + "\" to confirm:",
		run: func(typed string) string {
			if strings.TrimSpace(typed) != name {
				return "✗ name did not match — deletion cancelled"
			}
			if err := deleteSliceByName(name); err != nil {
				return "✗ " + err.Error()
			}
			if m.active == name { // clear the active slice if it was the one deleted
				_ = common.SaveActiveSlice("")
				m.active = ""
			}
			m.invalidateAll() // cached tab data may belong to the deleted slice
			m.loadSlices()
			if m.sideSel > len(m.slices) {
				m.sideSel = len(m.slices)
			}
			return "deleted slice " + name
		},
	}
}

// deleteLines is the roomy warning shown in the red right pane while confirming.
func (m *model) deleteLines() []string {
	return []string{
		"",
		" " + bold("Permanently delete slice ") + cRed + bold(m.deleting.name) + "?" + cReset,
		"",
		" " + dim("This destroys, with NO recovery:"),
		"   • every atomic function + canvas site",
		"   • the entire backbone — nosql, queues, blobs, secrets, cache",
		"   • all logs, metrics & deployment history",
		"   • the slice's database + isolated environment",
		"",
		" " + dim("There is no undo, and no backup to restore from."),
		"",
		" " + "Type the slice name below to confirm  ·  Esc to cancel.",
	}
}

// deleteSliceByName calls the control-plane delete endpoint for one slice.
func deleteSliceByName(name string) error {
	resp, err := common.DoRequest(http.MethodDelete,
		common.APIBaseURL+"/ops/slice/delete?name="+url.QueryEscape(name), nil)
	if err != nil {
		return common.TransportError("delete slice", err)
	}
	defer resp.Body.Close()
	_, err = common.CheckResponse(resp, "delete slice")
	return err
}
