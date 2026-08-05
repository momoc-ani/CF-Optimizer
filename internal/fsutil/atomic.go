package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// WriteFileAtomic 先写入同目录临时文件，再原子替换目标文件。
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeFileAtomic(path, data, perm, fileOwnership{})
}

// WriteFileAtomicPreservingMetadata 替换第三方已有文件时保留其 owner、group 和权限，新文件使用回退权限。
func WriteFileAtomicPreservingMetadata(path string, data []byte, fallbackPerm os.FileMode) error {
	permission := fallbackPerm
	ownership := fileOwnership{}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("preserve metadata for non-regular file %s", path)
		}
		permission = info.Mode().Perm()
		ownership, err = ownershipFromFileInfo(info)
		if err != nil {
			return fmt.Errorf("read ownership of %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read metadata of %s: %w", path, err)
	}
	return writeFileAtomic(path, data, permission, ownership)
}

// writeFileAtomic 在同目录准备完整临时文件，并在提交前应用指定所有权和权限。
func writeFileAtomic(path string, data []byte, perm os.FileMode, ownership fileOwnership) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cf-optimizer-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmp := f.Name()
	committed := false
	defer func() {
		_ = f.Close()
		if !committed {
			_ = os.Remove(tmp)
		}
	}()
	if err := applyFileOwnership(f, ownership); err != nil {
		return fmt.Errorf("set temporary file ownership: %w", err)
	}
	if err := f.Chmod(perm); err != nil {
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := replaceFile(tmp, path); err != nil {
		return err
	}
	committed = true
	return nil
}

func replaceFile(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) && runtime.GOOS != "windows" {
		return fmt.Errorf("replace %s: %w", destination, err)
	}

	backup := destination + ".replace-backup"
	_ = os.Remove(backup)
	if err := os.Rename(destination, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("prepare replacement of %s: %w", destination, err)
	}
	if err := os.Rename(source, destination); err != nil {
		_ = os.Rename(backup, destination)
		return fmt.Errorf("replace %s: %w", destination, err)
	}
	_ = os.Remove(backup)
	return nil
}

// CopyFileAtomic 读取源文件，并通过原子替换写入目标路径。
func CopyFileAtomic(source, destination string, perm os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return WriteFileAtomic(destination, data, perm)
}
