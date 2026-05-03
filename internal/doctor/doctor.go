// Package doctor inspects the local environment and reports whether
// each piece sunny depends on is ready: provider binaries, API keys,
// the daemon, and the on-disk runtime tree. Output is consumed by
// `sunny doctor` (rendered as a checklist) and by `sunny setup`,
// which uses the same probes to decide what flow a user needs.
//
// Probes are read-only and side-effect-free. They may exec
// `--version` against installed CLIs but never mutate state and
// never make authenticated network calls (key validation is
// deferred to `sunny setup` so plain `sunny doctor` doesn't burn
// tokens on every run).
package doctor

import "github.com/noesrafa/sunny/internal/secrets"

// Status is the tri-state outcome every probe returns. Callers map
// to ✓ / ⚠ / ✗ at render time.
type Status int

const (
	StatusOK Status = iota
	StatusWarn
	StatusFail
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusWarn:
		return "warn"
	default:
		return "fail"
	}
}

// Result is a single probe outcome. Detail is one short human line
// describing what we found ("v1.14.33 on PATH, 0 providers authed").
// Hint, when set, is a single command the user can run to fix what
// the probe flagged ("sunny setup opencode").
type Result struct {
	Name   string
	Status Status
	Detail string
	Hint   string
}

// Report bundles every probe so the renderer can lay them out in one
// pass. Providers are kept in declaration order; daemon and runtime
// each surface independently. Peers is one row per remote daemon
// configured in ~/.sunny/peers.yaml — empty for solo installs.
type Report struct {
	Providers []Result
	Daemon    Result
	Runtime   Result
	Peers     []Result
}

// Run executes every probe against the given runtime root. A missing
// secrets store is tolerated — provider probes treat it as "no key
// configured" rather than erroring.
func Run(root string) Report {
	store, _ := secrets.New(root)
	return Report{
		Providers: []Result{
			CheckClaudeCode(),
			CheckOpencode(),
			CheckAnthropic(store),
			CheckOllama(store),
		},
		Daemon:  CheckDaemon(root),
		Runtime: CheckRuntime(root),
		Peers:   CheckPeers(root),
	}
}
