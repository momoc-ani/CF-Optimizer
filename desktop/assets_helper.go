package desktop

import "io/fs"

// mustSub 将嵌入目录转换为以 index.html 为根的资源文件系统。
func mustSub(source fs.FS, directory string) fs.FS {
	assets, err := fs.Sub(source, directory)
	if err != nil {
		panic("desktop embedded assets: " + err.Error())
	}
	return assets
}
