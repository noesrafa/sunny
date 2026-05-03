package mesh

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerate_ProducesValid(t *testing.T) {
	for i := 0; i < 5; i++ {
		k, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if !k.Valid() {
			t.Errorf("Generate produced invalid key: %q", k)
		}
	}
}

func TestSaveLoad_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	k, _ := Generate()
	if err := Save(dir, k); err != nil {
		t.Fatalf("Save: %v", err)
	}
	stat, _ := os.Stat(filepath.Join(dir, FileName))
	if stat.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v, want 0600 (key is sensitive)", stat.Mode().Perm())
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != k {
		t.Errorf("roundtrip mismatch: got %q, want %q", got, k)
	}
}

func TestLoad_AbsentReturnsSentinel(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir)
	if !errors.Is(err, ErrAbsent) {
		t.Errorf("Load on missing file should return ErrAbsent, got %v", err)
	}
}

func TestLoad_RejectsCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil || errors.Is(err, ErrAbsent) {
		t.Errorf("Load on corrupt file should error (not absent), got %v", err)
	}
}

func TestSave_RejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Key("garbage")); err == nil {
		t.Errorf("Save should reject invalid key")
	}
}

func TestFingerprint_Stable(t *testing.T) {
	k := Key("abc-this-is-not-a-real-key-but-fingerprint-doesnt-care")
	if k.Fingerprint() != k.Fingerprint() {
		t.Errorf("Fingerprint should be deterministic")
	}
	if len(k.Fingerprint()) != 8 {
		t.Errorf("Fingerprint length = %d, want 8", len(k.Fingerprint()))
	}
}

func TestFingerprint_DifferentKeysDifferentPrints(t *testing.T) {
	k1, _ := Generate()
	k2, _ := Generate()
	if k1.Fingerprint() == k2.Fingerprint() {
		t.Errorf("two random keys produced same fingerprint (8 hex chars), astronomically unlikely")
	}
}

func TestEqual_ConstantTime(t *testing.T) {
	k1, _ := Generate()
	k2, _ := Generate()
	if !k1.Equal(k1) {
		t.Errorf("k.Equal(k) should be true")
	}
	if k1.Equal(k2) {
		t.Errorf("two random keys should not be equal")
	}
	if k1.Equal("") || k1.Equal("xx") {
		t.Errorf("Equal with shorter key returned true — constant-time semantics need same length")
	}
}

func TestValid(t *testing.T) {
	k, _ := Generate()
	if !k.Valid() {
		t.Errorf("Generated key reported invalid")
	}
	cases := map[Key]bool{
		"":                          false,
		"short":                     false,
		"!!!not_base64!!!":          false,
		"a" + Key(string(k)[1:]):    true, // hand-edited but still 32 decoded bytes (probably; size check passes)
	}
	for k, want := range cases {
		if got := k.Valid(); got != want {
			t.Errorf("Valid(%q) = %v, want %v", k, got, want)
		}
	}
}
