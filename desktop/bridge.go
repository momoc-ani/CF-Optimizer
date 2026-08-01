package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/ipc"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const requestTimeout = 30 * time.Second

var allowedMethods = map[string]struct{}{
	"system.status":         {},
	"optimizer.run":         {},
	"optimizer.cancel":      {},
	"quickstart.plan":       {},
	"quickstart.run":        {},
	"ranges.get":            {},
	"ranges.update":         {},
	"history.list":          {},
	"routes.list":           {},
	"proxy.detect":          {},
	"acceleration.domains":  {},
	"acceleration.discover": {},
	"diagnostics.route":     {},
	"config.get":            {},
	"config.update":         {},
	"logs.tail":             {},
}

// Bridge 将普通权限桌面界面的白名单业务请求转发到本地 IPC。
type Bridge struct {
	endpoint string
	mutex    sync.RWMutex
	ctx      context.Context
}

// NewBridge 创建仅连接指定本地端点的桌面桥接器。
func NewBridge(endpoint string) (*Bridge, error) {
	if endpoint == "" {
		return nil, errors.New("desktop IPC endpoint is required")
	}
	return &Bridge{endpoint: endpoint}, nil
}

// Startup 保存 Wails 生命周期上下文，供请求取消和事件转发使用。
func (b *Bridge) Startup(ctx context.Context) {
	b.mutex.Lock()
	b.ctx = ctx
	b.mutex.Unlock()
}

// Request 调用经过白名单约束的后台业务方法，并返回 JSON 文本。
func (b *Bridge) Request(method string, parameters map[string]any) (string, error) {
	if _, allowed := allowedMethods[method]; !allowed {
		return "", fmt.Errorf("desktop method %q is not allowed", method)
	}
	client, err := ipc.NewClient(b.endpoint)
	if err != nil {
		return "", err
	}
	requestContext := b.context()
	cancel := func() {}
	if method != "optimizer.run" && method != "quickstart.run" {
		requestContext, cancel = context.WithTimeout(requestContext, requestTimeout)
	}
	defer cancel()
	if parameters == nil {
		parameters = map[string]any{}
	}
	result, err := client.Call(requestContext, method, parameters, func(event json.RawMessage) error {
		b.mutex.RLock()
		ctx := b.ctx
		b.mutex.RUnlock()
		if ctx != nil {
			runtime.EventsEmit(ctx, "optimizer:event", string(event))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func (b *Bridge) context() context.Context {
	b.mutex.RLock()
	defer b.mutex.RUnlock()
	if b.ctx == nil {
		return context.Background()
	}
	return b.ctx
}
