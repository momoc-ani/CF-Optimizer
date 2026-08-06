package ipc

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestResponseScannerAcceptsLargeResultFrame(t *testing.T) {
	payload := strings.Repeat("x", maxRequestBytes+1024)
	frame := []byte(`{"version":1,"id":"request","type":"result","result":"` + payload + `"}` + "\n")
	scanner := newResponseScanner(bytes.NewReader(frame))
	if !scanner.Scan() {
		t.Fatalf("large response frame was rejected: %v", scanner.Err())
	}
	if scanner.Err() != nil || !bytes.Contains(scanner.Bytes(), []byte(payload)) {
		t.Fatal("large response frame was not read intact")
	}
}

func TestWriteFrameRejectsOversizedResponse(t *testing.T) {
	payload := strings.Repeat("x", maxResponseFrameBytes)
	err := writeFrame(io.Discard, &sync.Mutex{}, Frame{Version: ProtocolVersion, ID: "request", Type: "result", Result: []byte(`"` + payload + `"`)})
	if !errors.Is(err, errResponseFrameTooLarge) {
		t.Fatalf("writeFrame() error = %v, want oversized response error", err)
	}
}
