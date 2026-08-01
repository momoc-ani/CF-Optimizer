//go:build !production

package desktop

import "embed"

//go:embed fallback/*
var fallbackAssets embed.FS

// Assets 在开发和单元测试构建中提供不依赖 dist 的最小资源。
var Assets = mustSub(fallbackAssets, "fallback")
