package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileAtomicPreservingMetadataKeepsExistingPermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "third-party.yaml")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomicPreservingMetadata(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "after\n" {
		t.Fatalf("atomic content = %q", content)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("atomic permission = %o, want 644", info.Mode().Perm())
		}
	}
}

func TestWriteFileAtomicPreservingMetadataUsesFallbackForNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.yaml")
	if err := WriteFileAtomicPreservingMetadata(path, []byte("new\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Fatalf("new file permission = %o, want 640", info.Mode().Perm())
		}
	}
}

func TestWriteFileAtomicPreservingMetadataRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires optional Windows privileges")
	}
	directory := t.TempDir()
	target := filepath.Join(directory, "target.yaml")
	link := filepath.Join(directory, "third-party.yaml")
	if err := os.WriteFile(target, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomicPreservingMetadata(link, []byte("after\n"), 0o600); err == nil {
		t.Fatal("metadata-preserving replacement must reject a symlink")
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "before\n" {
		t.Fatalf("symlink target was modified: %q", content)
	}
}
