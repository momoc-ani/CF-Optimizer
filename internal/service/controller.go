package service

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/cf-optimizer/cf-optimizer/internal/config"
)

// Status 描述系统服务是否已安装、启用和运行。
type Status struct {
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	Detail    string `json:"detail,omitempty"`
}

// Controller 定义三个桌面平台统一的服务生命周期操作。
type Controller interface {
	Install(context.Context) error
	Uninstall(context.Context) error
	Start(context.Context) error
	Stop(context.Context) error
	Status(context.Context) (Status, error)
}

type controllerConfig struct {
	executable string
	configPath string
	config     config.Config
	timeout    time.Duration
}

// NewController 创建当前平台的服务控制器。
func NewController(executable, configPath string, cfg config.Config) (Controller, error) {
	if !filepath.IsAbs(executable) || !filepath.IsAbs(configPath) {
		return nil, errors.New("service executable and config paths must be absolute")
	}
	return newPlatformController(controllerConfig{executable: executable, configPath: configPath, config: cfg, timeout: cfg.Network.CommandTimeout.Duration()}), nil
}
