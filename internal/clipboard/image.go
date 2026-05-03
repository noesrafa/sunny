// Package clipboard reads images from the system clipboard.
//
// macOS-only for now: AppleScript (osascript) coerces the clipboard to «class
// PNGf» and dumps the bytes to a temp file we then slurp. Other platforms
// return ok=false silently — the TUI falls through to a plain text paste.
package clipboard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ReadImage returns PNG bytes from the system clipboard, if any.
// ok=false means "no image available" — not necessarily an error.
func ReadImage() (data []byte, mediaType string, ok bool, err error) {
	if runtime.GOOS != "darwin" {
		return nil, "", false, nil
	}
	return readDarwin()
}

func readDarwin() ([]byte, string, bool, error) {
	tmp, err := os.MkdirTemp("", "sunnytui-clip-")
	if err != nil {
		return nil, "", false, err
	}
	defer os.RemoveAll(tmp)
	path := filepath.Join(tmp, "clip.png")

	// AppleScript: try to coerce the clipboard to PNGf and dump it.
	// Unsupported types raise an error which we swallow into "no".
	script := fmt.Sprintf(`try
  set png to the clipboard as «class PNGf»
  set fh to open for access POSIX file %q with write permission
  set eof of fh to 0
  write png to fh
  close access fh
  return "ok"
on error
  try
    close access fh
  end try
  return "no"
end try`, path)

	out, runErr := exec.Command("osascript", "-e", script).Output()
	if runErr != nil {
		return nil, "", false, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "ok") {
		return nil, "", false, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", false, err
	}
	if len(b) == 0 {
		return nil, "", false, nil
	}
	return b, "image/png", true, nil
}

// ImagesDir is the on-disk location where pasted clipboard images are
// staged. Stable per user — callers may safely treat it as a cache dir.
func ImagesDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".sunnytui", "images"), nil
}

// PruneOrphans removes files in ImagesDir that aren't in `referenced`.
// Use this at startup (after loading saved sessions) so the directory
// doesn't grow unbounded as users paste-and-discard. Returns the number
// of files removed; missing-dir is not an error.
func PruneOrphans(referenced map[string]bool) (int, error) {
	dir, err := ImagesDir()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		full := filepath.Join(dir, e.Name())
		if referenced[full] {
			continue
		}
		if err := os.Remove(full); err == nil {
			removed++
		}
	}
	return removed, nil
}

// SaveImage persists clipboard image bytes under ~/.sunnytui/images/ and
// returns the absolute path. Filename includes a timestamp so multiple
// pastes within the same session don't collide.
func SaveImage(data []byte, mediaType string) (string, error) {
	dir, err := ImagesDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ext := ".png"
	if mediaType == "image/jpeg" {
		ext = ".jpg"
	}
	name := fmt.Sprintf("clip-%s%s", time.Now().Format("20060102-150405.000"), ext)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
