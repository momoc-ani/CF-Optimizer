package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/cf-optimizer/cf-optimizer/packaging/wails"
	"github.com/getlantern/systray"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	trayTitle       = "CF Optimizer"
	trayTooltip     = "CF Optimizer 后台服务管理界面"
	trayOpenLabel   = "打开 CF Optimizer"
	trayOpenTooltip = "显示管理界面"
	trayQuitLabel   = "退出界面"
	trayQuitTooltip = "退出桌面界面，后台服务继续运行"
)

type trayController struct {
	icon       []byte
	closed     chan struct{}
	closeOnce  sync.Once
	contextMu  sync.RWMutex
	appContext context.Context
}

// newTrayController 从共享应用图标创建托盘控制器，保证三个平台使用同一视觉标识。
func newTrayController() (*trayController, error) {
	icon, err := wailsassets.TrayIcon()
	if err != nil {
		return nil, fmt.Errorf("build system tray icon: %w", err)
	}
	return &trayController{icon: icon, closed: make(chan struct{})}, nil
}

// Register 在 Wails 启动其原生事件循环前注册系统托盘。
func (t *trayController) Register() {
	systray.Register(t.ready, nil)
}

// Startup 保存 Wails 运行时上下文，供托盘菜单安全地操作普通权限窗口。
func (t *trayController) Startup(ctx context.Context) {
	t.contextMu.Lock()
	t.appContext = ctx
	t.contextMu.Unlock()
}

// Shutdown 移除托盘资源并终止菜单监听，不影响独立后台服务。
func (t *trayController) Shutdown(context.Context) {
	t.contextMu.Lock()
	t.appContext = nil
	t.contextMu.Unlock()
	t.closeOnce.Do(func() {
		close(t.closed)
		systray.Quit()
	})
}

// ready 初始化固定尺寸菜单；关闭窗口后用户可从这里恢复界面或退出 UI。
func (t *trayController) ready() {
	systray.SetTemplateIcon(t.icon, t.icon)
	systray.SetTitle(trayTitle)
	systray.SetTooltip(trayTooltip)
	openItem := systray.AddMenuItem(trayOpenLabel, trayOpenTooltip)
	systray.AddSeparator()
	quitItem := systray.AddMenuItem(trayQuitLabel, trayQuitTooltip)
	go t.handleMenu(openItem, quitItem)
}

// handleMenu 串行处理托盘命令，避免窗口上下文与关闭流程发生竞态。
func (t *trayController) handleMenu(openItem, quitItem *systray.MenuItem) {
	for {
		select {
		case <-openItem.ClickedCh:
			if ctx := t.context(); ctx != nil {
				wailsruntime.WindowUnminimise(ctx)
				wailsruntime.WindowShow(ctx)
			}
		case <-quitItem.ClickedCh:
			if ctx := t.context(); ctx != nil {
				wailsruntime.Quit(ctx)
			}
			return
		case <-t.closed:
			return
		}
	}
}

func (t *trayController) context() context.Context {
	t.contextMu.RLock()
	defer t.contextMu.RUnlock()
	return t.appContext
}
