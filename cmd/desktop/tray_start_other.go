//go:build !darwin

package main

// prepareTrayStart 保留非 macOS 平台原有的 Wails 启动后初始化时序。
func prepareTrayStart(start func()) func() {
	return start
}
