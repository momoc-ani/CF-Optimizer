package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type systemProxyState string

const (
	systemProxyOff     systemProxyState = "off"
	systemProxyOn      systemProxyState = "on"
	systemProxyUnknown systemProxyState = "unknown"
)

type runtimeActivity struct {
	Ports []int
	TUN   bool
}

// runtimeActivity 读取 Mihomo 当前监听端口和 TUN 开关，不依据静态配置猜测运行状态。
func (a *Adapter) runtimeActivity(ctx context.Context) (runtimeActivity, error) {
	body, status, err := a.request(ctx, http.MethodGet, "/configs", nil)
	if err != nil {
		return runtimeActivity{}, fmt.Errorf("read Mihomo runtime activity: %w", err)
	}
	if status != http.StatusOK {
		return runtimeActivity{}, fmt.Errorf("Mihomo configs endpoint returned %d", status)
	}
	var document struct {
		Port      int `json:"port"`
		SocksPort int `json:"socks-port"`
		MixedPort int `json:"mixed-port"`
		TUN       struct {
			Enable bool `json:"enable"`
		} `json:"tun"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		return runtimeActivity{}, fmt.Errorf("decode Mihomo runtime activity: %w", err)
	}
	seen := map[int]struct{}{}
	ports := make([]int, 0, 3)
	for _, port := range []int{document.Port, document.SocksPort, document.MixedPort} {
		if port < 1 || port > 65535 {
			continue
		}
		if _, exists := seen[port]; exists {
			continue
		}
		seen[port] = struct{}{}
		ports = append(ports, port)
	}
	return runtimeActivity{Ports: ports, TUN: document.TUN.Enable}, nil
}

// proxySettingUsesPorts 判断系统代理文本是否指向本机 Mihomo 监听端口。
func proxySettingUsesPorts(raw string, ports []int) bool {
	allowed := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		allowed[port] = struct{}{}
	}
	for _, item := range strings.FieldsFunc(raw, func(character rune) bool {
		return character == ';' || character == ',' || character == ' '
	}) {
		if _, value, found := strings.Cut(item, "="); found {
			item = value
		}
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !strings.Contains(item, "://") {
			item = "http://" + item
		}
		parsed, err := url.Parse(item)
		if err != nil || !isLoopbackProxyHost(parsed.Hostname()) {
			continue
		}
		port, err := strconv.Atoi(parsed.Port())
		if err != nil {
			continue
		}
		if _, exists := allowed[port]; exists {
			return true
		}
	}
	return false
}

func isLoopbackProxyHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(strings.TrimSpace(host))
	return address != nil && address.IsLoopback()
}

func combineSystemProxyEvidence(states ...systemProxyState) systemProxyState {
	result := systemProxyUnknown
	for _, state := range states {
		if state == systemProxyOn {
			return systemProxyOn
		}
		if state == systemProxyOff {
			result = systemProxyOff
		}
	}
	return result
}

func containsPort(ports []int, expected int) bool {
	for _, port := range ports {
		if port == expected {
			return true
		}
	}
	return false
}

func commandUnavailable(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
