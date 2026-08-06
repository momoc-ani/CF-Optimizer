package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
)

const (
	// maxRequestBytes 限制单条 IPC 请求，避免客户端参数占用过多服务端内存。
	maxRequestBytes = 1 << 20
	// maxResponseFrameBytes 允许历史测速结果等单条响应携带有限的大 JSON 帧。
	maxResponseFrameBytes = 16 << 20
	// maxResponseStreamBytes 限制一次请求收到的事件和结果总量。
	maxResponseStreamBytes = 64 << 20
)

var errResponseFrameTooLarge = errors.New("IPC response frame exceeds the maximum size")

// Handler 处理已通过协议校验的业务请求，并可在返回前发送事件。
type Handler interface {
	Handle(context.Context, Request, func(any) error) (any, error)
}

// HandlerFunc 将函数适配为 Handler。
type HandlerFunc func(context.Context, Request, func(any) error) (any, error)

// Handle 调用被包装的业务函数。
func (f HandlerFunc) Handle(ctx context.Context, request Request, emit func(any) error) (any, error) {
	return f(ctx, request, emit)
}

// Server 在本地受限传输上提供版本化请求、响应和事件流。
type Server struct {
	endpoint string
	handler  Handler
	logger   *slog.Logger
}

// NewServer 创建本地 IPC 服务端。
func NewServer(endpoint string, handler Handler, logger *slog.Logger) (*Server, error) {
	if endpoint == "" || handler == nil || logger == nil {
		return nil, errors.New("IPC endpoint, handler and logger are required")
	}
	return &Server{endpoint: endpoint, handler: handler, logger: logger.With("component", "ipc")}, nil
}

// Serve 监听本地端点，直到上下文取消或监听器发生不可恢复错误。
func (s *Server) Serve(ctx context.Context) error {
	listener, cleanup, err := listenLocal(s.endpoint)
	if err != nil {
		return err
	}
	defer cleanup()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	var connections sync.WaitGroup
	defer connections.Wait()
	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			if ctx.Err() != nil || errors.Is(acceptErr, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept IPC connection: %w", acceptErr)
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			s.handleConnection(ctx, connection)
		}()
	}
}

func (s *Server) handleConnection(parent context.Context, connection net.Conn) {
	defer connection.Close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	scanner := bufio.NewScanner(io.LimitReader(connection, maxRequestBytes+1))
	scanner.Buffer(make([]byte, 4096), maxRequestBytes)
	if !scanner.Scan() {
		return
	}
	var request Request
	if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
		_ = writeFrame(connection, &sync.Mutex{}, Frame{Version: ProtocolVersion, Type: "error", Error: &Error{Code: "invalid_request", Message: "request is not valid JSON"}})
		return
	}
	if err := validateRequest(request); err != nil {
		protocolErr := protocolError(err)
		_ = writeFrame(connection, &sync.Mutex{}, Frame{Version: ProtocolVersion, ID: request.ID, Type: "error", Error: protocolErr})
		return
	}
	writerMutex := &sync.Mutex{}
	emit := func(event any) error {
		raw, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if err := writeFrame(connection, writerMutex, Frame{Version: ProtocolVersion, ID: request.ID, Type: "event", Event: raw}); err != nil {
			cancel()
			return err
		}
		return nil
	}
	result, err := s.handler.Handle(ctx, request, emit)
	if err != nil {
		protocolErr := protocolError(err)
		if writeErr := writeFrame(connection, writerMutex, Frame{Version: ProtocolVersion, ID: request.ID, Type: "error", Error: protocolErr}); writeErr != nil {
			s.logger.Debug("IPC 错误帧发送失败", "method", request.Method, "error", writeErr)
		}
		return
	}
	rawResult, err := json.Marshal(result)
	if err != nil {
		_ = writeFrame(connection, writerMutex, Frame{Version: ProtocolVersion, ID: request.ID, Type: "error", Error: &Error{Code: "internal", Message: "response serialization failed"}})
		return
	}
	if err := writeFrame(connection, writerMutex, Frame{Version: ProtocolVersion, ID: request.ID, Type: "result", Result: rawResult}); err != nil {
		if errors.Is(err, errResponseFrameTooLarge) {
			_ = writeFrame(connection, writerMutex, Frame{Version: ProtocolVersion, ID: request.ID, Type: "error", Error: &Error{Code: "response_too_large", Message: "IPC response exceeds the maximum frame size"}})
		}
		s.logger.Debug("IPC 结果帧发送失败", "method", request.Method, "error", err)
	}
}

func writeFrame(writer io.Writer, mutex *sync.Mutex, frame Frame) error {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(encoded)+1 > maxResponseFrameBytes {
		return fmt.Errorf("%w: %d bytes", errResponseFrameTooLarge, len(encoded)+1)
	}
	encoded = append(encoded, '\n')
	mutex.Lock()
	defer mutex.Unlock()
	_, err = writer.Write(encoded)
	return err
}

func protocolError(err error) *Error {
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	return &Error{Code: "internal", Message: err.Error()}
}
