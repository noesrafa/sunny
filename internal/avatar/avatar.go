// Package avatar handles per-agent avatar images stored at
// ~/.sunny/agents/<slug>/avatar.webp. Inputs are normalized: any
// supported source (PNG, JPEG, WebP) is decoded, scaled to fit a
// 512x512 square (cover semantics, centered crop), and re-encoded
// as lossless WebP. The on-disk format is always avatar.webp; the
// app is the only consumer.
package avatar

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"github.com/HugoSmits86/nativewebp"
	"golang.org/x/image/draw"
	xwebp "golang.org/x/image/webp"
)

// Filename is the canonical on-disk name. We always write WebP; other
// extensions left around by older versions or manual edits are ignored.
const Filename = "avatar.webp"

// MaxBytes caps the upload size. Sources beyond this are rejected
// before decoding to keep memory bounded — at 5 MiB you're already
// past anything a phone camera produces for a square crop.
const MaxBytes = 5 * 1024 * 1024

// Side is the output square edge (in pixels).
const Side = 512

// ErrTooLarge is returned by Process when the input exceeds MaxBytes.
var ErrTooLarge = errors.New("avatar: input exceeds 5 MiB")

// ErrUnsupported is returned by Process when the input bytes don't
// decode as PNG, JPEG, or WebP.
var ErrUnsupported = errors.New("avatar: unsupported image format (use png, jpg, or webp)")

// Process reads an image from r, decodes it, resizes to Side×Side
// using cover semantics (the shorter edge fills the square, the
// longer edge is center-cropped), and re-encodes as lossless WebP.
// The returned bytes are ready to write to disk.
func Process(r io.Reader) ([]byte, error) {
	limited := io.LimitReader(r, MaxBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read input: %w", err)
	}
	if len(buf) > MaxBytes {
		return nil, ErrTooLarge
	}
	img, err := decode(buf)
	if err != nil {
		return nil, err
	}
	square := centerCrop(img, Side)
	var out bytes.Buffer
	if err := nativewebp.Encode(&out, square, &nativewebp.Options{}); err != nil {
		return nil, fmt.Errorf("encode webp: %w", err)
	}
	return out.Bytes(), nil
}

// Save writes processed bytes (from Process) to dir/avatar.webp atomically.
func Save(dir string, data []byte) error {
	target := filepath.Join(dir, Filename)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write avatar: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("rename avatar: %w", err)
	}
	return nil
}

// Remove deletes the avatar file from dir if it exists. Idempotent.
func Remove(dir string) error {
	err := os.Remove(filepath.Join(dir, Filename))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Path returns the on-disk avatar path if it exists, else "".
func Path(dir string) string {
	p := filepath.Join(dir, Filename)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

func decode(buf []byte) (image.Image, error) {
	// Try every codec we support. The image package's auto-detect
	// (image.Decode) only sees what's been imported; we explicitly
	// dispatch to avoid registering side-effect imports on every
	// caller of this package.
	if img, err := png.Decode(bytes.NewReader(buf)); err == nil {
		return img, nil
	}
	if img, err := jpeg.Decode(bytes.NewReader(buf)); err == nil {
		return img, nil
	}
	if img, err := xwebp.Decode(bytes.NewReader(buf)); err == nil {
		return img, nil
	}
	return nil, ErrUnsupported
}

// centerCrop scales src so the shorter side equals side, then crops
// the longer side from the center. Output is RGBA at side×side.
func centerCrop(src image.Image, side int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	// Pick a scale factor so min(w,h)*scale == side. The other
	// dimension overshoots side and is cropped after the resize.
	scale := float64(side) / float64(min(w, h))
	rw := int(float64(w)*scale + 0.5)
	rh := int(float64(h)*scale + 0.5)
	resized := image.NewRGBA(image.Rect(0, 0, rw, rh))
	draw.CatmullRom.Scale(resized, resized.Bounds(), src, b, draw.Over, nil)

	out := image.NewRGBA(image.Rect(0, 0, side, side))
	offX := (rw - side) / 2
	offY := (rh - side) / 2
	draw.Draw(out, out.Bounds(), resized, image.Point{X: offX, Y: offY}, draw.Src)
	return out
}
