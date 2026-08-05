//go:build windows

package fsutil

import "os"

type fileOwnership struct{}

// ownershipFromFileInfo 在 Windows 上保留接口边界；文件 DACL 由平台替换语义管理。
func ownershipFromFileInfo(os.FileInfo) (fileOwnership, error) { return fileOwnership{}, nil }

// applyFileOwnership 在 Windows 上不调用不受支持的 Unix Chown。
func applyFileOwnership(*os.File, fileOwnership) error { return nil }
