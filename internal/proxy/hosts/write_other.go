//go:build !windows

package hosts

import (
	"os"

	"github.com/cf-optimizer/cf-optimizer/internal/fsutil"
)

// writeHostsFile 在支持原子替换的平台写入完整受管 Hosts 内容。
func writeHostsFile(path string, content, _ []byte, permission os.FileMode) error {
	return fsutil.WriteFileAtomic(path, content, permission)
}
