//go:build production

package desktop

import "embed"

//go:embed all:frontend/dist
var productionAssets embed.FS

// Assets 包含由 Vite 生成并嵌入正式桌面程序的静态前端资源。
var Assets = mustSub(productionAssets, "frontend/dist")
