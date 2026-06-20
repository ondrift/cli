package portal

// The "+ New slice" flow has two paths, chosen from a small modal:
//
//   Empty slice      → the configurator (build the envelope by hand).
//   From a Driftfile → a minimal directory explorer to locate a Driftfile,
//                      then SUSPEND the dashboard and run the real
//                      `drift project deploy` (full build/upload pipeline,
//                      streaming output), then re-enter.
//
// A slice is the remote envelope (what the sidebar lists); a Driftfile is the
// local declarative project that provisions + fills one. Reuse the proven
// deploy pipeline rather than reimplementing it inside the TUI.

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ondrift/cli/common"
	"golang.org/x/term"
)

// ─── chooser ─────────────────────────────────────────────────────────────────

// chooser kinds: the same two-button modal serves "+ New slice" and the
// Atomic tab's "+ New function".
const (
	chooserSlice = iota
	chooserFn
)

type chooserOption struct{ label, help string }

type newChooser struct {
	sel  int
	kind int
}

var sliceChooserOpts = []chooserOption{
	{"Empty slice", "Configure the resource envelope by hand (the configurator)."},
	{"From a Driftfile", "Find a Driftfile, then provision its slice + deploy the whole project."},
}

var fnChooserOpts = []chooserOption{
	{"Deploy a directory", "Pick a folder that holds an atomic function and deploy it."},
	{"Scaffold new", "Pick a folder, then scaffold a brand-new function there (drift atomic new)."},
}

func (c *newChooser) opts() []chooserOption {
	if c.kind == chooserFn {
		return fnChooserOpts
	}
	return sliceChooserOpts
}

// creatingFunction reports whether the active modal is the new-function flow (so
// the right pane titles "New function" in orange rather than "New slice" green).
func (m *model) creatingFunction() bool {
	return (m.chooser != nil && m.chooser.kind == chooserFn) ||
		(m.explorer != nil && m.explorer.mode != exDriftfile)
}

func (m *model) handleChooser(k key) bool {
	c := m.chooser
	switch k {
	case keyQuit, keyBack:
		m.chooser = nil
		m.status = "cancelled"
	case keyUp:
		if c.sel > 0 {
			c.sel--
		}
	case keyDown:
		if c.sel < len(c.opts())-1 {
			c.sel++
		}
	case keyEnter:
		kind, sel := c.kind, c.sel
		m.chooser = nil
		switch {
		case kind == chooserSlice && sel == 0: // build the envelope by hand
			m.form = newConfigForm()
			m.recomputePrice()
		case kind == chooserSlice: // from a Driftfile
			m.openExplorer(exDriftfile)
		case sel == 0: // function: deploy an existing directory
			m.openExplorer(exFnDeploy)
		default: // function: scaffold a new one
			m.openExplorer(exFnNew)
		}
	}
	return false
}

func (m *model) chooserLines() []string {
	c := m.chooser
	opts := c.opts()
	title, col, bg := bold(cGreen+"+ New slice"+cReset), cBlue, bgBlue
	if c.kind == chooserFn {
		title, col, bg = bold(cOrange+"+ New function"+cReset), cOrange, bgOrange
	}
	out := []string{title, ""}
	// Two real brand buttons side by side; the selected one slams flat (pressed).
	a := button(opts[0].label, col, bg, c.sel == 0, false)
	d := button(opts[1].label, col, bg, c.sel == 1, false)
	aw := 0
	for _, ln := range a {
		if v := vlen(ln); v > aw {
			aw = v
		}
	}
	for i := range a {
		out = append(out, pad(a[i], aw)+"   "+d[i])
	}
	out = append(out, "", dim(opts[c.sel].help))
	return out
}

// ─── directory explorer ──────────────────────────────────────────────────────

const (
	entryFound     = iota // a Driftfile discovered nearby by the open-time scan
	entryParent           // ".."
	entryDir              // a sub-directory
	entryDriftfile        // a Driftfile in the current directory
	entrySelectDir        // function modes: "use this directory" (deploy / scaffold)
)

// explorer modes — what "select" ultimately does.
const (
	exDriftfile = iota // a Driftfile → drift project deploy (provision a slice)
	exFnDeploy         // a directory  → drift atomic deploy <dir>
	exFnNew            // a directory  → drift atomic new (scaffold there)
)

type fileEntry struct {
	kind  int
	label string // display name
	path  string // dir to cd into / deploy from
}

type fileExplorer struct {
	mode    int
	dir     string
	entries []fileEntry
	found   []string // dirs holding a Driftfile, from the open-time scan (Driftfile mode)
	sel     int
	err     string
}

func (m *model) openExplorer(mode int) {
	dir, err := os.Getwd()
	if err != nil {
		dir = os.Getenv("HOME")
	}
	e := &fileExplorer{mode: mode}
	if mode == exDriftfile {
		e.found = scanDriftfiles(dir)
	}
	e.cd(dir)
	if mode == exDriftfile && len(e.found) > 0 {
		e.sel = 0 // land on the first quick-pick so it's obvious
	}
	m.explorer = e
}

// cd lists dir. Driftfile mode: scan quick-picks, then "..", any Driftfile here,
// then sub-directories. Function modes: a "use this directory" action leads the
// list, then ".." + sub-directories. Plain files are always hidden.
func (e *fileExplorer) cd(dir string) {
	e.dir, e.err = dir, ""
	var entries []fileEntry
	focus := 0
	if e.mode == exDriftfile {
		for _, d := range e.found {
			entries = append(entries, fileEntry{kind: entryFound, label: abbrevHome(d), path: d})
		}
		focus = len(entries) // land past the quick-picks, on the current dir
	} else {
		entries = append(entries, fileEntry{kind: entrySelectDir, label: abbrevHome(dir), path: dir})
	}

	ents, err := os.ReadDir(dir)
	if err != nil {
		e.err = err.Error()
		e.entries, e.sel = entries, 0
		return
	}
	entries = append(entries, fileEntry{kind: entryParent, label: "..", path: filepath.Dir(dir)})
	if e.mode == exDriftfile {
		for _, ent := range ents {
			if ent.Name() == "Driftfile" && !ent.IsDir() {
				entries = append(entries, fileEntry{kind: entryDriftfile, label: "Driftfile", path: dir})
			}
		}
	}
	for _, ent := range ents {
		if ent.IsDir() && !strings.HasPrefix(ent.Name(), ".") {
			entries = append(entries, fileEntry{kind: entryDir, label: ent.Name(), path: filepath.Join(dir, ent.Name())})
		}
	}
	e.entries, e.sel = entries, focus
}

// scanDriftfiles looks for Driftfiles near cwd: from cwd plus up to three
// ancestors, walking each up to three levels deep. Bounded + skips heavy/hidden
// dirs so it stays instant. Returns the directories that hold a Driftfile.
func scanDriftfiles(cwd string) []string {
	roots := []string{cwd}
	for d := cwd; len(roots) <= 3; {
		p := filepath.Dir(d)
		if p == d {
			break
		}
		roots = append(roots, p)
		d = p
	}
	visited := map[string]bool{}
	budget := 800
	var found []string
	for _, root := range roots {
		walkDriftfiles(root, 3, visited, &budget, &found)
	}
	return found
}

func walkDriftfiles(dir string, depth int, visited map[string]bool, budget *int, found *[]string) {
	if depth < 0 || *budget <= 0 || len(*found) >= 12 || visited[dir] {
		return
	}
	visited[dir] = true
	*budget--
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() && e.Name() == "Driftfile" {
			*found = append(*found, dir)
			break
		}
	}
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !skipScanDir(e.Name()) {
			walkDriftfiles(filepath.Join(dir, e.Name()), depth-1, visited, budget, found)
		}
	}
}

func skipScanDir(name string) bool {
	switch name {
	case "node_modules", "vendor", "target", "dist", "build", "__pycache__":
		return true
	}
	return false
}

// abbrevHome shortens a path under $HOME to ~/… for display.
func abbrevHome(p string) string {
	if home := os.Getenv("HOME"); home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// expandUser resolves a leading ~ to $HOME.
func expandUser(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home := os.Getenv("HOME"); home != "" {
			return home + strings.TrimPrefix(p, "~")
		}
	}
	return p
}

func (m *model) handleExplorer(k key) bool {
	e := m.explorer
	switch k {
	case keyQuit, keyBack:
		m.explorer = nil
		m.status = "cancelled"
	case keyUp:
		if e.sel > 0 {
			e.sel--
		}
	case keyDown:
		if e.sel < len(e.entries)-1 {
			e.sel++
		}
	case keyLeft:
		e.cd(filepath.Dir(e.dir))
	case keySet, keyCreate: // 'g'-style: type a path to jump to
		m.input = &inputPrompt{
			label: "go to path:",
			buf:   e.dir,
			run: func(s string) string {
				e.cd(expandUser(s))
				return ""
			},
		}
	case keyEnter, keyRight:
		if e.sel >= len(e.entries) {
			return false
		}
		switch ent := e.entries[e.sel]; ent.kind {
		case entryParent, entryDir:
			e.cd(ent.path)
		case entryFound, entryDriftfile:
			m.deployDriftfile(ent.path)
		case entrySelectDir:
			if e.mode == exFnNew {
				m.scaffoldFunction(ent.path)
			} else {
				m.deployFunction(ent.path)
			}
		}
	}
	return false
}

func (m *model) explorerLines() []string {
	e := m.explorer
	header, hint := bold(cGreen+"+ New slice "+cReset+dim("· from a Driftfile")), "g type a path · ★ Driftfile found nearby"
	switch e.mode {
	case exFnDeploy:
		header, hint = bold(cOrange+"+ New function "+cReset+dim("· deploy a directory")), "g type a path · ● select to deploy"
	case exFnNew:
		header, hint = bold(cOrange+"+ New function "+cReset+dim("· scaffold new")), "g type a path · ● select to scaffold here"
	}
	out := []string{
		header,
		"",
		dim(abbrevHome(e.dir)),
		dim(hint),
		"",
	}
	if e.err != "" {
		return append(out, "\x1b[31m✗ "+e.err+"\x1b[0m")
	}
	win := m.contentH - 6
	if win < 3 {
		win = 3
	}
	start := 0
	if e.sel >= win {
		start = e.sel - win + 1
	}
	end := min(start+win, len(e.entries))
	for i := start; i < end; i++ {
		ent := e.entries[i]
		var plain, colored string
		switch ent.kind {
		case entryFound:
			plain, colored = "★ "+ent.label+"  → deploy", cGreen+"★ "+ent.label+cReset+dim("  → deploy")
		case entryParent:
			plain, colored = "..", cBlue+".."+cReset
		case entryDir:
			plain, colored = ent.label+"/", cBlue+ent.label+"/"+cReset
		case entryDriftfile:
			plain, colored = "Driftfile  → deploy", cGreen+"Driftfile"+cReset+dim("  → deploy")
		case entrySelectDir:
			action := "→ deploy this directory"
			if e.mode == exFnNew {
				action = "→ scaffold here"
			}
			plain, colored = "● use this directory  "+action, cOrange+"● use this directory"+cReset+dim("  "+action)
		}
		if i == e.sel {
			out = append(out, rev(" "+plain+" "))
		} else {
			out = append(out, "  "+colored)
		}
	}
	if len(e.entries) > win {
		out = append(out, "", dim(fmt.Sprintf("  %d/%d", e.sel+1, len(e.entries))))
	}
	return out
}

// ─── suspend → deploy → resume ───────────────────────────────────────────────

// driftExe resolves our own binary (so the suspended runs use the same build).
func driftExe() string {
	exe, err := os.Executable()
	if err != nil {
		return "drift"
	}
	return exe
}

// suspendAndRun leaves the alt-screen + raw mode, runs cmd with the terminal
// wired straight through (so interactive prompts + streamed output work), waits
// for acknowledgement, then re-enters the dashboard and runs refresh. okMsg /
// failMsg become the status line.
func (m *model) suspendAndRun(banner string, cmd *exec.Cmd, okMsg, failMsg string, refresh func()) {
	_ = term.Restore(m.fd, m.oldState) // #nosec G104 -- best-effort
	fmt.Print("\x1b[?25h\x1b[?1049l")  // show cursor, leave alt-screen
	fmt.Printf("\n\x1b[1m%s\x1b[0m\n\n", banner)

	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := cmd.Run()

	if runErr != nil {
		fmt.Printf("\n\x1b[31mfailed: %v\x1b[0m\n", runErr)
	}
	fmt.Print("\n  [ press Enter to return to the dashboard ]")
	waitForEnter()

	fmt.Print("\x1b[?1049h\x1b[?25l") // re-enter the alt-screen
	if st, e := term.MakeRaw(m.fd); e == nil {
		m.oldState = st
	}
	if refresh != nil {
		refresh()
	}
	if runErr == nil {
		m.status = okMsg
	} else {
		m.status = failMsg
	}
}

// deployDriftfile suspends the dashboard, runs the real `drift project deploy`
// in dir, then re-enters with a fresh slice list.
func (m *model) deployDriftfile(dir string) {
	m.explorer = nil
	cmd := exec.Command(driftExe(), "project", "deploy") // #nosec G204 -- our own binary, fixed args
	cmd.Dir = dir
	m.suspendAndRun("drift project deploy  "+abbrevHome(dir), cmd,
		"deployed "+filepath.Base(dir), "✗ deploy failed (see output above)",
		func() { m.active = common.GetActiveSlice(); m.loadSlices() })
}

// deployFunction suspends the dashboard, runs `drift atomic deploy <dir>` against
// the active slice, then re-enters with a fresh function list.
func (m *model) deployFunction(dir string) {
	m.explorer = nil
	cmd := exec.Command(driftExe(), "atomic", "deploy", dir) // #nosec G204 -- our own binary, fixed args
	m.suspendAndRun("drift atomic deploy  "+abbrevHome(dir), cmd,
		"deployed function from "+filepath.Base(dir), "✗ deploy failed (see output above)",
		func() { m.invalidateAll(); m.load(m.tab) })
}

// scaffoldFunction suspends the dashboard, runs the interactive `drift atomic
// new` in dir (it prompts for name/language/trigger), then re-enters.
func (m *model) scaffoldFunction(dir string) {
	m.explorer = nil
	cmd := exec.Command(driftExe(), "atomic", "new") // #nosec G204 -- our own binary, fixed args
	cmd.Dir = dir
	m.suspendAndRun("drift atomic new  (in "+abbrevHome(dir)+")", cmd,
		"scaffolded in "+filepath.Base(dir)+" — deploy it with [N]", "✗ scaffold cancelled / failed",
		func() { m.invalidateAll(); m.load(m.tab) })
}

// waitForEnter blocks until the user presses Enter (cooked-mode read).
func waitForEnter() {
	r := bufio.NewReader(os.Stdin)
	for {
		b, err := r.ReadByte()
		if err != nil || b == '\n' || b == '\r' {
			return
		}
	}
}
