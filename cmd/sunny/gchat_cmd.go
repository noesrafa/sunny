package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/noesrafa/sunny/internal/gchat"
)

// gchatCmd is the CLI surface for the Google Chat integration:
//
//	sunny gchat auth --credentials <path>   → one-time OAuth consent.
//	                                          Copies the client JSON
//	                                          into the integration dir
//	                                          and saves the resulting
//	                                          refresh token.
//	sunny gchat test                        → list visible spaces using
//	                                          the saved token. Sanity
//	                                          check that auth + network
//	                                          + scopes all work.
//	sunny gchat status                      → does the integration have
//	                                          a token on disk? What
//	                                          scopes does it have?
//
// No daemon involvement yet — this is a standalone tool. The monitor
// source that consumes the same package lands in a follow-up.
func gchatCmd(args []string) error {
	if len(args) == 0 {
		return gchatUsage()
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "auth":
		return gchatAuth(rest)
	case "test":
		return gchatTest(rest)
	case "status":
		return gchatStatus(rest)
	case "help", "-h", "--help":
		return gchatUsage()
	}
	return fmt.Errorf("unknown gchat subcommand: %s", sub)
}

func gchatUsage() error {
	fmt.Fprintln(os.Stderr, `usage: sunny gchat <subcommand>

subcommands:
  auth --credentials <path>   Run the one-time OAuth consent flow.
                              <path> points at the OAuth client JSON
                              downloaded from Google Cloud Console.
  test                        List the spaces the authenticated user
                              can see. Verifies the saved token works.
  status                      Show whether a token is saved and where.

flags:
  --root   sunny runtime directory (default ~/.sunny)`)
	return nil
}

func gchatAuth(args []string) error {
	fs := flag.NewFlagSet("gchat auth", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	credentials := fs.String("credentials", "", "path to the OAuth client JSON from Google Cloud Console")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *credentials == "" {
		return fmt.Errorf("--credentials <path> is required (download from console.cloud.google.com → APIs & Services → Credentials)")
	}

	cfg, err := gchat.LoadConfig(*credentials, gchat.ScopeSpacesReadonly)
	if err != nil {
		return err
	}

	// Copy the JSON into the integration dir first so even if the
	// consent flow fails mid-way, the next retry can find the
	// credentials without re-passing --credentials.
	if err := gchat.SaveCredentials(*root, *credentials); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	fmt.Println("credentials saved →", gchat.CredentialsPath(*root))

	// Honour Ctrl+C while waiting for the browser consent — without
	// this the loopback server keeps running after the user gives up.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tok, err := gchat.Authorize(ctx, cfg)
	if err != nil {
		return err
	}
	if err := gchat.SaveToken(*root, tok); err != nil {
		return fmt.Errorf("save token: %w", err)
	}
	fmt.Println("token saved      →", gchat.TokenPath(*root))
	fmt.Println("scopes           →", strings.Join(cfg.Scopes, ", "))
	fmt.Println("\nnext: sunny gchat test")
	return nil
}

func gchatTest(args []string) error {
	fs := flag.NewFlagSet("gchat test", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	c, err := gchat.New(ctx, *root, gchat.ScopeSpacesReadonly)
	if err != nil {
		return err
	}
	spaces, err := c.ListSpaces(ctx)
	if err != nil {
		return err
	}
	if len(spaces) == 0 {
		fmt.Println("(no spaces visible to the authenticated user)")
		return nil
	}
	fmt.Printf("connected — %d space(s) visible:\n\n", len(spaces))
	for _, s := range spaces {
		label := s.DisplayName
		if label == "" {
			label = "(no name — likely a DM)"
		}
		fmt.Printf("  %-30s  %-8s  %s\n", trunc(label, 30), s.Type, s.Name)
	}
	return nil
}

func gchatStatus(args []string) error {
	fs := flag.NewFlagSet("gchat status", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")
	if err := fs.Parse(args); err != nil {
		return err
	}

	credPath := gchat.CredentialsPath(*root)
	tokPath := gchat.TokenPath(*root)
	credOk := fileExists(credPath)
	tokOk := fileExists(tokPath)

	mark := func(ok bool) string {
		if ok {
			return "✓"
		}
		return "✗"
	}

	fmt.Printf("%s credentials   %s\n", mark(credOk), credPath)
	fmt.Printf("%s token         %s\n", mark(tokOk), tokPath)
	if !credOk || !tokOk {
		fmt.Println("\nrun: sunny gchat auth --credentials <path>")
	}
	return nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
