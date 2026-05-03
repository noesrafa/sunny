package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/noesrafa/sunny/internal/mesh"
)

// meshCmd is the CLI surface for ~/.sunny/mesh.key:
//
//	sunny mesh                → show fingerprint (no key value)
//	sunny mesh export         → print key to stdout (for copy/paste)
//	sunny mesh import [<key>] → save key from arg or stdin
//	sunny mesh rotate         → generate a NEW key (existing peers
//	                            must re-import)
//
// The key is the credential a TUI on another tailnet host needs to
// auto-trust this daemon. Distribute it once per machine you want
// in the mesh; after that, discovery is automatic.
func meshCmd(args []string) error {
	fs := flag.NewFlagSet("mesh", flag.ExitOnError)
	root := fs.String("root", defaultRoot(), "sunny runtime directory")

	// Allow flags on either side of the subcommand (Go's flag parser
	// stops at the first positional otherwise). Same pattern as
	// setup/pair commands.
	var positional []string
	var flagArgs []string
	skip := false
	for i, a := range args {
		if skip {
			skip = false
			flagArgs = append(flagArgs, a)
			continue
		}
		if strings.HasPrefix(a, "-") {
			flagArgs = append(flagArgs, a)
			if !strings.Contains(a, "=") && i+1 < len(args) {
				skip = true
			}
			continue
		}
		positional = append(positional, a)
	}
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	if len(positional) == 0 {
		return meshShow(*root)
	}
	switch positional[0] {
	case "export":
		return meshExport(*root)
	case "import":
		var raw string
		if len(positional) >= 2 {
			raw = positional[1]
		}
		return meshImport(*root, raw)
	case "rotate":
		return meshRotate(*root)
	}
	return fmt.Errorf("unknown subcommand: %s", strings.Join(positional, " "))
}

// meshShow prints the fingerprint (NOT the key) so the operator
// can confirm two hosts share a mesh without leaking the key.
func meshShow(root string) error {
	k, err := mesh.Load(root)
	if errors.Is(err, mesh.ErrAbsent) {
		fmt.Println("(no mesh configured — start the daemon to auto-generate, or run `sunny mesh import <key>`)")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("fingerprint: %s\n", k.Fingerprint())
	fmt.Println("\nTo add another machine to this mesh:")
	fmt.Println("  sunny mesh export | ssh other-host sunny mesh import")
	fmt.Println("\nOr copy the key manually:")
	fmt.Println("  sunny mesh export")
	return nil
}

// meshExport prints the raw key to stdout so the operator can pipe
// it to another `sunny mesh import` (the obvious idiom; no need
// for a fancy QR code at this stage).
func meshExport(root string) error {
	k, err := mesh.Load(root)
	if err != nil {
		return err
	}
	fmt.Println(string(k))
	return nil
}

// meshImport accepts the key as an arg, or reads it from stdin
// when no arg is given. Refuses to overwrite a different existing
// key without --force (not implemented — failing loud is the v0.16
// contract; we add --force when somebody actually hits this).
func meshImport(root, raw string) error {
	if raw == "" {
		v, err := readSecretValue("mesh.key")
		if err != nil {
			return err
		}
		raw = v
	}
	k := mesh.Key(strings.TrimSpace(raw))
	if !k.Valid() {
		return fmt.Errorf("not a valid mesh key (want a base64url-encoded 32-byte value)")
	}
	if existing, err := mesh.Load(root); err == nil {
		if existing.Equal(k) {
			fmt.Printf("✓ already imported (fingerprint %s)\n", k.Fingerprint())
			return nil
		}
		fmt.Fprintln(os.Stderr, "warning: a different mesh key was already configured")
		fmt.Fprintf(os.Stderr, "         old fingerprint: %s\n", existing.Fingerprint())
		fmt.Fprintf(os.Stderr, "         new fingerprint: %s\n", k.Fingerprint())
		fmt.Fprintln(os.Stderr, "         (overwriting — old key revoked from this host)")
	}
	if err := mesh.Save(root, k); err != nil {
		return err
	}
	fmt.Printf("✓ imported (fingerprint %s)\n", k.Fingerprint())
	fmt.Println("\nRestart the daemon so it picks up the new key:")
	fmt.Println("  sunny stop && sunny start")
	return nil
}

// meshRotate generates a fresh key on this host. EVERY peer that
// wants to stay in the mesh must re-import. We surface the new
// fingerprint so the operator can sanity-check the rollout.
func meshRotate(root string) error {
	k, err := mesh.Generate()
	if err != nil {
		return err
	}
	if err := mesh.Save(root, k); err != nil {
		return err
	}
	fmt.Printf("✓ rotated (new fingerprint %s)\n", k.Fingerprint())
	fmt.Println()
	fmt.Println("Distribute the new key to every host you want in this mesh:")
	fmt.Println("  sunny mesh export | ssh other-host sunny mesh import")
	fmt.Println("\nUntil they re-import, those hosts fall back to manual pairing.")
	return nil
}
