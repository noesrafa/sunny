package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/noesrafa/sunny/internal/doctor"
)

// doctorCmd implements `sunny doctor`: a single-screen checklist of
// what's installed, configured, and running. Designed to be the first
// thing a new user runs after `brew install sunny`.
//
// Output is plain text + ASCII glyphs (✓ / ⚠ / ✗) so it copy-pastes
// cleanly into bug reports. We deliberately don't pull in a TUI lib;
// any user already in trouble doesn't need a TUI on top.
func doctorCmd(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	report := doctor.Run(*root)
	renderReport(os.Stdout, report)
	return nil
}

// renderReport prints the report. Sections are separated by a blank
// line; each entry is "  GLYPH  NAME    DETAIL", with hints (when
// present) on a continuation line so the eye lands on the action.
func renderReport(w io.Writer, r doctor.Report) {
	fmt.Fprintln(w, "Providers")
	for _, p := range r.Providers {
		writeRow(w, p)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Daemon")
	writeBareRow(w, r.Daemon)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Runtime")
	writeBareRow(w, r.Runtime)
	if r.Tailscale != nil {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Tailscale")
		writeBareRow(w, *r.Tailscale)
	}
	if len(r.Peers) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Peers")
		for _, p := range r.Peers {
			writeRow(w, p)
		}
	}

	if pending := nextSteps(r); len(pending) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Next steps")
		for _, s := range pending {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
}

func writeRow(w io.Writer, r doctor.Result) {
	fmt.Fprintf(w, "  %s  %-12s %s\n", glyph(r.Status), r.Name, r.Detail)
	if r.Hint != "" {
		fmt.Fprintf(w, "                    → %s\n", r.Hint)
	}
}

// writeBareRow is for sections where the section header already names
// the thing being inspected (Daemon, Runtime) — repeating the name in
// the row would just be noise.
func writeBareRow(w io.Writer, r doctor.Result) {
	fmt.Fprintf(w, "  %s  %s\n", glyph(r.Status), r.Detail)
	if r.Hint != "" {
		fmt.Fprintf(w, "       → %s\n", r.Hint)
	}
}

func glyph(s doctor.Status) string {
	switch s {
	case doctor.StatusOK:
		return "✓"
	case doctor.StatusWarn:
		return "⚠"
	default:
		return "✗"
	}
}

// nextSteps surfaces only the actionable hints, deduped, in
// declaration order. Useful when the user wants the TL;DR after
// scanning the table.
func nextSteps(r doctor.Report) []string {
	var out []string
	seen := map[string]bool{}
	add := func(hint string) {
		hint = strings.TrimSpace(hint)
		if hint == "" || seen[hint] {
			return
		}
		seen[hint] = true
		out = append(out, hint)
	}
	for _, p := range r.Providers {
		if p.Status != doctor.StatusOK {
			add(p.Hint)
		}
	}
	if r.Daemon.Status != doctor.StatusOK {
		add(r.Daemon.Hint)
	}
	if r.Runtime.Status != doctor.StatusOK {
		add(r.Runtime.Hint)
	}
	for _, p := range r.Peers {
		if p.Status != doctor.StatusOK {
			add(p.Hint)
		}
	}
	if r.Tailscale != nil && r.Tailscale.Status != doctor.StatusOK {
		add(r.Tailscale.Hint)
	}
	return out
}
