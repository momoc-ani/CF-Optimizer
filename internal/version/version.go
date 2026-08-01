package version

// 构建流水线通过 ldflags 覆盖以下值。
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Metadata 返回适合 CLI、IPC 和诊断报告展示的构建信息。
func Metadata() map[string]string {
	return map[string]string{"version": Version, "commit": Commit, "date": Date}
}
