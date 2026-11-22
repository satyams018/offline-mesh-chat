package crypto

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestLoadOrCreateIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "node")

	first, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, identityFile))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("identity file mode = %o, want 600", perm)
	}

	second, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if first.ID() != second.ID() {
		t.Error("reloaded identity has a different node ID")
	}
}

func TestLoadOrCreateIdentityRejectsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, identityFile), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateIdentity(dir); err == nil {
		t.Fatal("expected an error for a corrupt identity file")
	}
}

func TestFingerprint(t *testing.T) {
	id, err := NewIdentity(bytes.NewReader(make([]byte, 64)))
	if err != nil {
		t.Fatal(err)
	}
	fp := id.ID().Fingerprint()
	if !regexp.MustCompile(`^[A-Z2-7]{4}(-[A-Z2-7]{4}){3}$`).MatchString(fp) {
		t.Errorf("fingerprint %q has the wrong shape", fp)
	}
	if got := NormalizeFingerprint(" " + fp[:9] + " "); got != (fp[0:4] + fp[5:9]) {
		t.Errorf("NormalizeFingerprint = %q", got)
	}
}

func TestNodeIDFromBytes(t *testing.T) {
	if _, err := NodeIDFromBytes(make([]byte, 31)); err == nil {
		t.Error("31 bytes accepted")
	}
	if _, err := NodeIDFromBytes(make([]byte, NodeIDSize)); err != nil {
		t.Errorf("32 bytes rejected: %v", err)
	}
}
