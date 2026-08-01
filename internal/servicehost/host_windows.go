//go:build windows

package servicehost

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/windows/svc"
)

const windowsServiceName = "CFOptimizer"

type windowsHandler struct {
	run      func(context.Context) error
	runError error
}

func (h *windowsHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- h.run(ctx) }()
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-finished:
			h.runError = err
			return false, 1
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case err := <-finished:
					h.runError = err
				case <-time.After(30 * time.Second):
					h.runError = errors.New("service shutdown timed out")
				}
				return false, 0
			}
		}
	}
}

// Run 在 SCM 环境注册服务处理器，否则按控制台进程执行。
func Run(ctx context.Context, service func(context.Context) error) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isService {
		return service(ctx)
	}
	handler := &windowsHandler{run: service}
	if err := svc.Run(windowsServiceName, handler); err != nil {
		return err
	}
	return handler.runError
}
