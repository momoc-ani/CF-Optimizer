//go:build !linux && !darwin && !windows

package service

import (
	"context"
	"fmt"
	"runtime"
)

type unsupportedController struct{}

func newPlatformController(controllerConfig) Controller { return unsupportedController{} }

func (unsupportedController) Install(context.Context) error   { return unsupportedServiceError() }
func (unsupportedController) Uninstall(context.Context) error { return unsupportedServiceError() }
func (unsupportedController) Start(context.Context) error     { return unsupportedServiceError() }
func (unsupportedController) Stop(context.Context) error      { return unsupportedServiceError() }
func (unsupportedController) Status(context.Context) (Status, error) {
	return Status{}, unsupportedServiceError()
}

func unsupportedServiceError() error {
	return fmt.Errorf("system service management is not supported on %s", runtime.GOOS)
}
