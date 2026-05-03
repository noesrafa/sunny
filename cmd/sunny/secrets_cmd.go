package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/noesrafa/sunny/internal/secrets"
)

// secretsCmd is the CLI surface for ~/.sunny/secrets.yaml. Subcommands:
//
//	sunny secrets                       → list configured providers + fields
//	sunny secrets <provider>            → show fields for that provider
//	sunny secrets <provider> set <fld>  → read value from stdin, save
//	sunny secrets <provider> delete     → remove provider section
//
// `set` reads from stdin so the value never lands in shell history /
// `ps`. Pipe it: `pbpaste | sunny secrets anthropic set api_key`,
// or run without stdin and the prompt asks interactively.
//
// Writes go straight to ~/.sunny/secrets.yaml. The running daemon's
// provider drivers consult the store fresh on every Stream call so
// the new value takes effect without a daemon restart — but the
// daemon's *registry* (which providers exist) only updates on next
// boot or after a PUT /secrets via HTTP.
func secretsCmd(args []string) error {
	fs := flag.NewFlagSet("secrets", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	store, err := secrets.New(*root)
	if err != nil {
		return err
	}

	switch len(rest) {
	case 0:
		return secretsList(store)
	case 1:
		return secretsShow(store, rest[0])
	}
	provider := rest[0]
	switch rest[1] {
	case "set":
		if len(rest) != 3 {
			return fmt.Errorf("usage: sunny secrets %s set <field>", provider)
		}
		return secretsSet(store, provider, rest[2])
	case "delete", "remove", "rm":
		if err := store.Delete(provider); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", provider)
		return nil
	}
	return fmt.Errorf("unknown subcommand: %s", strings.Join(rest[1:], " "))
}

func secretsList(s *secrets.Store) error {
	infos := s.List()
	if len(infos) == 0 {
		fmt.Println("(no secrets configured)")
		fmt.Println("try: sunny secrets anthropic set api_key")
		return nil
	}
	for _, i := range infos {
		fmt.Printf("%-12s %s\n", i.Provider, strings.Join(i.Fields, ", "))
	}
	return nil
}

func secretsShow(s *secrets.Store, provider string) error {
	for _, i := range s.List() {
		if i.Provider == provider {
			if len(i.Fields) == 0 {
				fmt.Println("(no fields)")
				return nil
			}
			for _, f := range i.Fields {
				fmt.Printf("%s ✓\n", f)
			}
			return nil
		}
	}
	fmt.Printf("%s: not configured\n", provider)
	return nil
}

func secretsSet(s *secrets.Store, provider, field string) error {
	value, err := readSecretValue(provider, field)
	if err != nil {
		return err
	}
	if value == "" {
		return fmt.Errorf("empty value — refusing to save")
	}
	if err := s.SetField(provider, field, value); err != nil {
		return err
	}
	fmt.Printf("saved %s.%s\n", provider, field)
	fmt.Fprintln(os.Stderr, "(running daemon picks up the new value on next request; no restart needed)")
	return nil
}

// readSecretValue reads a secret value from stdin. When stdin is a
// pipe, reads everything; when it's a TTY, prompts interactively
// (echo is NOT suppressed — recommend piping for sensitive values;
// we deliberately don't pull in a terminal-control dep just for this).
func readSecretValue(provider, field string) (string, error) {
	stat, _ := os.Stdin.Stat()
	piped := (stat.Mode() & os.ModeCharDevice) == 0
	if piped {
		raw, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	fmt.Fprintf(os.Stderr, "value for %s.%s (will echo — pipe for sensitive values): ", provider, field)
	var line string
	if _, err := fmt.Fscanln(os.Stdin, &line); err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}
	return strings.TrimSpace(line), nil
}
