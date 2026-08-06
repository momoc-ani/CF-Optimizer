package ipc

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Client 通过当前平台的本地安全传输调用后台服务。
type Client struct{ endpoint string }

// NewClient 创建指向指定本地端点的 IPC 客户端。
func NewClient(endpoint string) (*Client, error) {
	if endpoint == "" {
		return nil, errors.New("IPC endpoint is required")
	}
	return &Client{endpoint: endpoint}, nil
}

// Call 发送一个请求，持续消费事件帧，直到收到结果或错误。
func (c *Client) Call(ctx context.Context, method string, parameters any, onEvent func(json.RawMessage) error) (json.RawMessage, error) {
	connection, err := dialLocal(ctx, c.endpoint)
	if err != nil {
		return nil, fmt.Errorf("connect daemon: %w", err)
	}
	defer connection.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-done:
		}
	}()
	params, err := json.Marshal(parameters)
	if err != nil {
		return nil, err
	}
	request := Request{Version: ProtocolVersion, ID: newRequestID(), Method: method, Params: params}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := connection.Write(append(encoded, '\n')); err != nil {
		return nil, fmt.Errorf("write IPC request: %w", err)
	}
	scanner := newResponseScanner(connection)
	for scanner.Scan() {
		var frame Frame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return nil, fmt.Errorf("decode IPC frame: %w", err)
		}
		if frame.Version != ProtocolVersion || frame.ID != request.ID {
			return nil, errors.New("IPC response version or request ID mismatch")
		}
		switch frame.Type {
		case "event":
			if onEvent != nil {
				if err := onEvent(frame.Event); err != nil {
					return nil, err
				}
			}
		case "result":
			return frame.Result, nil
		case "error":
			if frame.Error == nil {
				return nil, errors.New("IPC error frame has no error body")
			}
			return nil, frame.Error
		default:
			return nil, fmt.Errorf("unknown IPC frame type %q", frame.Type)
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return nil, fmt.Errorf("read IPC response: %w", err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, io.ErrUnexpectedEOF
}

// newResponseScanner 为 IPC 响应设置单帧和整条响应流的有限上限。
func newResponseScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(io.LimitReader(reader, maxResponseStreamBytes))
	scanner.Buffer(make([]byte, 4096), maxResponseFrameBytes)
	return scanner
}

func newRequestID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return "local-request"
}
