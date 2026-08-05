//go:build !windows

package fsutil

import (
	"errors"
	"os"
	"syscall"
)

type fileOwnership struct {
	userID  int
	groupID int
	valid   bool
}

// ownershipFromFileInfo 读取 Unix 文件的数值 owner 和 group，避免依赖名称解析。
func ownershipFromFileInfo(info os.FileInfo) (fileOwnership, error) {
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileOwnership{}, errors.New("file status does not contain Unix ownership")
	}
	return fileOwnership{userID: int(status.Uid), groupID: int(status.Gid), valid: true}, nil
}

// applyFileOwnership 仅在临时文件 owner 或 group 不同时执行 Chown。
func applyFileOwnership(file *os.File, ownership fileOwnership) error {
	if !ownership.valid {
		return nil
	}
	current, err := file.Stat()
	if err != nil {
		return err
	}
	currentOwnership, err := ownershipFromFileInfo(current)
	if err != nil {
		return err
	}
	if currentOwnership.userID == ownership.userID && currentOwnership.groupID == ownership.groupID {
		return nil
	}
	return file.Chown(ownership.userID, ownership.groupID)
}
