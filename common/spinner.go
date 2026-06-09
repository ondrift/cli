package common

import (
	"fmt"
	"sync"
	"time"
)

// spinnerFrames is the standard braille-dot animation used by most modern
// JavaScript and Rust CLIs (Angular, Vite, Vercel, Astro, …).
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerColor matches the success checkmark color so the in-progress
// indicator visually morphs into the final ✓ when work completes.
const spinnerColor = "\x1b[38;2;16;185;129m" // #10b981

// Spinner renders a single-line animation next to a label by repeatedly
// rewriting the current line with \r. It is intentionally minimal: no
// nested spinners, no out-of-band logging while spinning. Callers should
// avoid writing to stdout between Start and Stop.
//
// When stdout is not a terminal (or NO_COLOR is set), Spinner becomes a
// no-op: Start renders nothing and Stop just leaves the cursor where it is,
// so piped output stays clean and machine-parseable.
type Spinner struct {
	label  string
	indent string
	stopCh chan struct{}
	doneCh chan struct{}
	active bool
	mu     sync.Mutex
}

// StartSpinner kicks off a background goroutine that animates the given
// label after `indent` spaces of leading whitespace. Call Stop to clear the
// line before printing the persistent success/failure marker.
func StartSpinner(indent, label string) *Spinner {
	s := &Spinner{
		label:  label,
		indent: indent,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	if !IsStyleEnabled() {
		// Non-TTY: still need a valid Stop, but no animation.
		close(s.doneCh)
		return s
	}
	s.active = true
	go s.run()
	return s
}

// run drives the animation loop until Stop closes stopCh.
func (s *Spinner) run() {
	defer close(s.doneCh)

	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()

	i := 0
	for {
		select {
		case <-s.stopCh:
			s.clearLine()
			return
		case <-ticker.C:
			s.mu.Lock()
			label := s.label
			s.mu.Unlock()
			frame := spinnerFrames[i%len(spinnerFrames)]
			fmt.Printf("\r%s%s%s%s %s", s.indent, spinnerColor, frame, reset, label)
			i++
		}
	}
}

// Update changes the label of an in-flight spinner. Useful when a single
// step transitions through several phases (e.g. "building" → "uploading").
func (s *Spinner) Update(label string) {
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}

// Stop halts the animation and clears the spinner line so the caller can
// print the final persistent line in its place. Safe to call multiple times.
func (s *Spinner) Stop() {
	if !s.active {
		return
	}
	s.active = false
	close(s.stopCh)
	<-s.doneCh
}

// clearLine erases the current terminal line and returns the cursor to the
// start so the caller's next write begins from column 0.
func (s *Spinner) clearLine() {
	fmt.Print("\r\x1b[2K")
}
