package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/noesrafa/sunny/internal/auth"
)

// token prints the current bearer token, or rotates it.
//
// Usage:
//
//	sunny token         → print the current token
//	sunny token rotate  → generate a new one, print it, invalidate the old
func token(args []string) error {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	switch {
	case len(rest) == 0:
		tok, err := auth.LoadToken(*root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("no token at %s — start the daemon at least once first", auth.Path(*root))
			}
			return err
		}
		fmt.Println(tok)
		return nil
	case len(rest) == 1 && rest[0] == "rotate":
		tok, err := auth.Rotate(*root)
		if err != nil {
			return err
		}
		fmt.Println(tok)
		fmt.Fprintln(os.Stderr, "rotated — restart the daemon (`sunny stop && sunny start`) and any open TUI sessions for it to take effect")
		return nil
	default:
		return fmt.Errorf("unknown token subcommand: %s", strings.Join(rest, " "))
	}
}
