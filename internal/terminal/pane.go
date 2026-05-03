// Package terminal hosts a PTY-backed terminal pane, used to embed full TUIs
// (claude code interactive, lazygit, etc.) inside sunnytui as an alternate
// tab type. Built on top of:
//
//   - github.com/creack/pty for spawning + Setsize
//   - github.com/hinshun/vt10x for VT100/xterm parsing + cell grid
package terminal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

// Pane wraps one PTY-backed child process and a vt10x emulator. It is
// goroutine-safe: Feed/Send/Resize/Close all use mu.
type Pane struct {
	ID      string
	Title   string
	Cwd     string
	Command string

	term vt10x.Terminal
	pty  *os.File
	cmd  *exec.Cmd

	mu      sync.Mutex
	width   int
	height  int
	alive   bool
	exitErr error
}

var idSeq atomic.Int64

func nextID() string {
	return fmt.Sprintf("p%d", idSeq.Add(1))
}

// Spawn starts the command via `sh -c` in a fresh PTY of size (cols, rows).
// `name` is the human-readable label, `cwd` is optional.
func Spawn(name, command, cwd string, cols, rows int) (*Pane, error) {
	if cols < 20 {
		cols = 80
	}
	if rows < 5 {
		rows = 24
	}
	if command == "" {
		return nil, errors.New("command required")
	}

	term := vt10x.New(vt10x.WithSize(cols, rows))

	c := exec.Command("sh", "-c", command)
	if cwd != "" {
		c.Dir = cwd
	}
	c.Env = append(os.Environ(),
		"TERM=xterm-256color",
		// Tell the child its size up-front so things like `claude` don't
		// initialize at the parent's tty dims.
		fmt.Sprintf("COLUMNS=%d", cols),
		fmt.Sprintf("LINES=%d", rows),
	)
	// New process group so Stop can kill the whole tree.
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	f, err := pty.StartWithSize(c, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, fmt.Errorf("pty.StartWithSize: %w", err)
	}

	p := &Pane{
		ID:      nextID(),
		Title:   name,
		Cwd:     cwd,
		Command: command,
		term:    term,
		pty:     f,
		cmd:     c,
		width:   cols,
		height:  rows,
		alive:   true,
	}
	go p.waitExit()
	return p, nil
}

func (p *Pane) waitExit() {
	err := p.cmd.Wait()
	p.mu.Lock()
	p.alive = false
	p.exitErr = err
	p.mu.Unlock()
	_ = p.pty.Close()
}

// ReadOnce performs one blocking read off the PTY and feeds bytes into the
// emulator. Returns (n, err); err is io.EOF when the child exits.
func (p *Pane) ReadOnce(buf []byte) (int, error) {
	n, err := p.pty.Read(buf)
	if n > 0 {
		p.mu.Lock()
		_, _ = p.term.Write(buf[:n])
		p.mu.Unlock()
	}
	return n, err
}

// Send writes user input bytes to the PTY (forwarded to the child stdin).
func (p *Pane) Send(b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.alive {
		return errors.New("pane closed")
	}
	_, err := p.pty.Write(b)
	return err
}

// Resize updates both vt10x's grid and the kernel-level PTY size, which
// triggers SIGWINCH in the child.
func (p *Pane) Resize(cols, rows int) {
	if cols < 20 {
		cols = 20
	}
	if rows < 5 {
		rows = 5
	}
	p.mu.Lock()
	p.width = cols
	p.height = rows
	p.term.Resize(cols, rows)
	p.mu.Unlock()
	_ = pty.Setsize(p.pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Size returns the current pane dimensions.
func (p *Pane) Size() (cols, rows int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.width, p.height
}

// Alive reports whether the child process is still running.
func (p *Pane) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.alive
}

// ExitErr returns the wait-time error (or nil if exited cleanly).
func (p *Pane) ExitErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitErr
}

// Close attempts a graceful TERM, then KILL after a short grace.
func (p *Pane) Close() error {
	p.mu.Lock()
	if !p.alive || p.cmd == nil || p.cmd.Process == nil {
		p.mu.Unlock()
		return nil
	}
	pid := p.cmd.Process.Pid
	p.mu.Unlock()
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_, _ = p.cmd.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	return nil
}

// LockTerm gives external callers (the renderer) shared access to the
// underlying vt10x.Terminal. Caller MUST call UnlockTerm when done.
func (p *Pane) LockTerm() vt10x.Terminal {
	p.mu.Lock()
	return p.term
}
func (p *Pane) UnlockTerm() { p.mu.Unlock() }

// Cursor returns the (x, y) the embedded child wants the caret at, plus a
// flag for whether it should be visible (some TUIs hide cursor in alt-screen
// menus). Coords are pane-relative.
func (p *Pane) Cursor() (x, y int, visible bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.term == nil {
		return 0, 0, false
	}
	c := p.term.Cursor()
	return c.X, c.Y, p.term.CursorVisible()
}
