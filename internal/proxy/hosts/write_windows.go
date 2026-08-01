//go:build windows

package hosts

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
)

// writeHostsFile 在不依赖 rename 的同一句柄上复核并写入 Windows Hosts，兼容不共享删除权限的读取进程。
func writeHostsFile(path string, content, expectedCurrent []byte, _ os.FileMode) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open Windows Hosts for update: %w", err)
	}
	current, err := io.ReadAll(file)
	if err != nil {
		return errors.Join(fmt.Errorf("read Windows Hosts before update: %w", err), wrapHostsCloseError(file.Close()))
	}
	if !bytes.Equal(current, expectedCurrent) {
		return errors.Join(errors.New("Windows Hosts changed immediately before write"), wrapHostsCloseError(file.Close()))
	}
	if err := rewriteOpenHostsFile(file, content); err != nil {
		restoreErr := rewriteOpenHostsFile(file, expectedCurrent)
		closeErr := file.Close()
		return errors.Join(fmt.Errorf("write Windows Hosts: %w", err), wrapHostsRestoreError(restoreErr), wrapHostsCloseError(closeErr))
	}
	if err := file.Close(); err != nil {
		restoreErr := restoreClosedHostsFile(path, expectedCurrent)
		return errors.Join(fmt.Errorf("close Windows Hosts: %w", err), wrapHostsRestoreError(restoreErr))
	}
	return nil
}

// rewriteOpenHostsFile 在同一已锁定句柄中截断、写入并强制刷新 Hosts。
func rewriteOpenHostsFile(file *os.File, content []byte) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		return err
	}
	return file.Sync()
}

// restoreClosedHostsFile 在关闭失败句柄后尽力恢复事务前内容。
func restoreClosedHostsFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	restoreErr := rewriteOpenHostsFile(file, content)
	return errors.Join(restoreErr, file.Close())
}

// wrapHostsRestoreError 为恢复失败补充稳定的 Hosts 事务上下文。
func wrapHostsRestoreError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("restore Windows Hosts after write failure: %w", err)
}

// wrapHostsCloseError 为句柄关闭失败补充稳定的 Hosts 事务上下文。
func wrapHostsCloseError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close Windows Hosts after restore: %w", err)
}
