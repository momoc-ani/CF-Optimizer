package optimizer

import (
	"context"
	"sync"
)

// operationGate 串行化会修改路由或代理策略的操作，并允许完整优选可取消地等待维护任务结束。
type operationGate struct {
	once  sync.Once
	token chan struct{}
}

// acquire 等待取得执行权，并在调用方取消时及时退出。
func (g *operationGate) acquire(ctx context.Context) error {
	g.initialize()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.token:
	}
	if err := ctx.Err(); err != nil {
		g.release()
		return err
	}
	return nil
}

// tryAcquire 尝试立即取得后台维护执行权。
func (g *operationGate) tryAcquire() bool {
	g.initialize()
	select {
	case <-g.token:
		return true
	default:
		return false
	}
}

// release 归还唯一执行令牌；存在等待中的完整优选时令牌会直接交给等待者。
func (g *operationGate) release() {
	g.initialize()
	g.token <- struct{}{}
}

func (g *operationGate) initialize() {
	g.once.Do(func() {
		g.token = make(chan struct{}, 1)
		g.token <- struct{}{}
	})
}
