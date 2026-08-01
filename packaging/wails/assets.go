package wailsassets

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"sync"
)

const trayIconSize = 32

//go:embed appicon.png
var applicationIconPNG []byte

var cachedTrayIcon = sync.OnceValues(buildTrayIcon)

// TrayIcon 返回由正式应用图标缩放得到的 ICO 数据，供三个桌面平台的托盘复用。
func TrayIcon() ([]byte, error) {
	return cachedTrayIcon()
}

// buildTrayIcon 使用 PNG 图像作为 ICO 载荷，保留透明度并兼容现代 Windows Shell 与 GTK。
func buildTrayIcon() ([]byte, error) {
	source, err := png.Decode(bytes.NewReader(applicationIconPNG))
	if err != nil {
		return nil, fmt.Errorf("decode application icon: %w", err)
	}
	resized := image.NewNRGBA(image.Rect(0, 0, trayIconSize, trayIconSize))
	bounds := source.Bounds()
	for y := 0; y < trayIconSize; y++ {
		for x := 0; x < trayIconSize; x++ {
			sourceX := bounds.Min.X + (2*x+1)*bounds.Dx()/(2*trayIconSize)
			sourceY := bounds.Min.Y + (2*y+1)*bounds.Dy()/(2*trayIconSize)
			resized.Set(x, y, source.At(sourceX, sourceY))
		}
	}
	var payload bytes.Buffer
	if err := png.Encode(&payload, resized); err != nil {
		return nil, fmt.Errorf("encode tray icon: %w", err)
	}

	icon := make([]byte, 22, 22+payload.Len())
	binary.LittleEndian.PutUint16(icon[2:4], 1)
	binary.LittleEndian.PutUint16(icon[4:6], 1)
	icon[6] = trayIconSize
	icon[7] = trayIconSize
	binary.LittleEndian.PutUint16(icon[10:12], 1)
	binary.LittleEndian.PutUint16(icon[12:14], 32)
	binary.LittleEndian.PutUint32(icon[14:18], uint32(payload.Len()))
	binary.LittleEndian.PutUint32(icon[18:22], 22)
	return append(icon, payload.Bytes()...), nil
}
