//go:build !windows

package fsutil

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteFileAtomicPreservingMetadataKeepsOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "third-party.yaml")
	if err := os.WriteFile(path, []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	targetGroup := os.Getegid()
	for _, group := range groups {
		if group != os.Getegid() {
			targetGroup = group
			break
		}
	}
	if targetGroup == os.Getegid() {
		t.Skip("current user has no supplementary group for ownership preservation test")
	}
	if err := os.Chown(path, os.Getuid(), targetGroup); err != nil {
		t.Skipf("cannot prepare alternate owned group: %v", err)
	}
	if err := WriteFileAtomicPreservingMetadata(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("file status does not contain Unix ownership")
	}
	if int(status.Uid) != os.Getuid() || int(status.Gid) != targetGroup {
		t.Fatalf("atomic ownership = %d:%d, want %d:%d", status.Uid, status.Gid, os.Getuid(), targetGroup)
	}
}
