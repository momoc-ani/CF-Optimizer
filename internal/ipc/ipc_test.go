//go:build linux || darwin

package ipc

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerStreamsEventAndResult(t *testing.T) {
	endpoint := filepath.Join(t.TempDir(), "daemon.sock")
	handler := HandlerFunc(func(_ context.Context, request Request, emit func(any) error) (any, error) {
		if request.Method != "echo" {
			t.Fatalf("unexpected method %s", request.Method)
		}
		if err := emit(map[string]string{"stage": "test"}); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, nil
	})
	server, err := NewServer(endpoint, handler, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverError := make(chan error, 1)
	go func() { serverError <- server.Serve(ctx) }()
	client, _ := NewClient(endpoint)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(endpoint); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("IPC socket was not created")
		}
		time.Sleep(time.Millisecond)
	}
	events := 0
	result, err := client.Call(context.Background(), "echo", map[string]string{"value": "x"}, func(raw json.RawMessage) error {
		events++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if events != 1 || string(result) != `{"ok":true}` {
		t.Fatalf("unexpected IPC response: events=%d result=%s", events, result)
	}
	cancel()
	if err := <-serverError; err != nil {
		t.Fatal(err)
	}
}
