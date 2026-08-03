//go:build darwin

package main

// prepareTrayStart 在 AppKit 主线程进入 Wails 事件循环前创建 macOS 状态栏资源。
func prepareTrayStart(start func()) func() {
	if start != nil {
		start()
	}
	return nil
}
