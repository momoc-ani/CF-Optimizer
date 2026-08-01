package ipc

import (
	"encoding/json"
	"fmt"
)

// ProtocolVersion 在客户端与服务端不兼容时提供明确拒绝。
const ProtocolVersion = 1

// Request 是一条经过版本化的本地 IPC 请求。
type Request struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Error 描述客户端可分类处理的协议错误。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Frame 是结果、事件或错误三种服务端帧的统一外层。
type Frame struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Result  json.RawMessage `json:"result,omitempty"`
	Event   json.RawMessage `json:"event,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

func validateRequest(request Request) error {
	if request.Version != ProtocolVersion {
		return &Error{Code: "incompatible_version", Message: fmt.Sprintf("client version %d, server version %d", request.Version, ProtocolVersion)}
	}
	if request.ID == "" || len(request.ID) > 128 {
		return &Error{Code: "invalid_request", Message: "request ID is required and must not exceed 128 bytes"}
	}
	if request.Method == "" || len(request.Method) > 128 {
		return &Error{Code: "invalid_request", Message: "method is required and must not exceed 128 bytes"}
	}
	return nil
}
