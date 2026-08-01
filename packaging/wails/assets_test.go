package wailsassets

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestTrayIconContainsExpectedPNG(t *testing.T) {
	icon, err := TrayIcon()
	if err != nil {
		t.Fatal(err)
	}
	if len(icon) <= 22 || binary.LittleEndian.Uint16(icon[2:4]) != 1 || icon[6] != trayIconSize || icon[7] != trayIconSize {
		t.Fatalf("unexpected ICO directory: %v", icon[:22])
	}
	payloadSize := int(binary.LittleEndian.Uint32(icon[14:18]))
	if payloadSize != len(icon)-22 || binary.LittleEndian.Uint32(icon[18:22]) != 22 {
		t.Fatalf("unexpected ICO payload metadata: size=%d offset=%d", payloadSize, binary.LittleEndian.Uint32(icon[18:22]))
	}
	decoded, err := png.Decode(bytes.NewReader(icon[22:]))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != trayIconSize || decoded.Bounds().Dy() != trayIconSize {
		t.Fatalf("unexpected tray icon dimensions: %v", decoded.Bounds())
	}
}
