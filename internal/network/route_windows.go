//go:build windows

package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"time"
)

type windowsRouteBackend struct{ timeout time.Duration }

type windowsRouteRecord struct {
	DestinationPrefix string `json:"DestinationPrefix"`
	NextHop           string `json:"NextHop"`
	InterfaceAlias    string `json:"InterfaceAlias"`
	InterfaceIndex    int    `json:"InterfaceIndex"`
	RouteMetric       int    `json:"RouteMetric"`
	IPAddress         string `json:"IPAddress"`
}

func newPlatformRouteBackend(commandTimeout time.Duration) RouteBackend {
	return &windowsRouteBackend{timeout: commandTimeout}
}

func (b *windowsRouteBackend) Replace(ctx context.Context, route RouteSpec) error {
	index, err := windowsInterfaceIndex(route)
	if err != nil {
		return err
	}
	family := "IPv6"
	if netip.MustParsePrefix(route.Prefix).Addr().Is4() {
		family = "IPv4"
	}
	command := fmt.Sprintf(
		"$ErrorActionPreference='Stop'; $old=Get-NetRoute -AddressFamily %s -DestinationPrefix '%s' -ErrorAction SilentlyContinue; if($old){$old|Remove-NetRoute -Confirm:$false}; New-NetRoute -AddressFamily %s -DestinationPrefix '%s' -NextHop '%s' -InterfaceIndex %d -RouteMetric %d -PolicyStore ActiveStore | Out-Null",
		family, route.Prefix, family, route.Prefix, route.Gateway, index, route.Metric,
	)
	_, err = b.runPowerShell(ctx, command)
	return err
}

func (b *windowsRouteBackend) Delete(ctx context.Context, route RouteSpec) error {
	family := "IPv6"
	if netip.MustParsePrefix(route.Prefix).Addr().Is4() {
		family = "IPv4"
	}
	command := fmt.Sprintf("$route=Get-NetRoute -AddressFamily %s -DestinationPrefix '%s' -ErrorAction SilentlyContinue; if(-not $route){exit 44}; $route|Remove-NetRoute -Confirm:$false -ErrorAction Stop", family, route.Prefix)
	_, err := b.runPowerShell(ctx, command)
	if isPowerShellExit(err, 44) {
		return ErrRouteNotFound
	}
	return err
}

func (b *windowsRouteBackend) Get(ctx context.Context, prefix string) (RouteSpec, error) {
	family := "IPv6"
	if netip.MustParsePrefix(prefix).Addr().Is4() {
		family = "IPv4"
	}
	command := fmt.Sprintf("$route=Get-NetRoute -AddressFamily %s -DestinationPrefix '%s' -ErrorAction SilentlyContinue|Select-Object -First 1 DestinationPrefix,NextHop,InterfaceAlias,InterfaceIndex,RouteMetric; if(-not $route){exit 44}; $route|ConvertTo-Json -Compress", family, prefix)
	output, err := b.runPowerShell(ctx, command)
	if isPowerShellExit(err, 44) {
		return RouteSpec{}, ErrRouteNotFound
	}
	if err != nil {
		return RouteSpec{}, err
	}
	record, err := decodeWindowsRoute(output)
	if err != nil {
		return RouteSpec{}, err
	}
	return record.RouteSpec(), nil
}

func (b *windowsRouteBackend) Resolve(ctx context.Context, target netip.Addr) (ResolvedRoute, error) {
	command := fmt.Sprintf("[array]$records=Find-NetRoute -RemoteIPAddress '%s' -ErrorAction Stop|Select-Object DestinationPrefix,NextHop,InterfaceAlias,InterfaceIndex,RouteMetric,IPAddress; if(-not $records){exit 44}; ConvertTo-Json -InputObject $records -Compress", target)
	output, err := b.runPowerShell(ctx, command)
	if isPowerShellExit(err, 44) {
		return ResolvedRoute{}, ErrRouteNotFound
	}
	if err != nil {
		return ResolvedRoute{}, err
	}
	return decodeWindowsResolvedRoute(output)
}

func (b *windowsRouteBackend) runPowerShell(ctx context.Context, script string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	encodingPrefix := "[Console]::OutputEncoding=[Text.UTF8Encoding]::new();"
	output, err := exec.CommandContext(commandContext, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", encodingPrefix+script).CombinedOutput()
	if err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("PowerShell timed out: %w", commandContext.Err())
		}
		return nil, fmt.Errorf("PowerShell failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func windowsInterfaceIndex(route RouteSpec) (int, error) {
	return resolveWindowsInterfaceIndex(route, net.InterfaceByName, net.InterfaceByIndex)
}

// resolveWindowsInterfaceIndex 优先用稳定接口名刷新可能因重连而变化的 Windows 接口索引。
func resolveWindowsInterfaceIndex(
	route RouteSpec,
	byName func(string) (*net.Interface, error),
	byIndex func(int) (*net.Interface, error),
) (int, error) {
	var nameErr error
	if strings.TrimSpace(route.Interface) != "" {
		iface, err := byName(route.Interface)
		if err == nil {
			return iface.Index, nil
		}
		nameErr = err
	}
	if route.InterfaceIndex > 0 {
		iface, err := byIndex(route.InterfaceIndex)
		if err == nil {
			return iface.Index, nil
		}
		if nameErr != nil {
			return 0, fmt.Errorf("resolve Windows interface %q or index %d: %w", route.Interface, route.InterfaceIndex, errors.Join(nameErr, err))
		}
		return 0, fmt.Errorf("resolve Windows interface index %d: %w", route.InterfaceIndex, err)
	}
	if nameErr != nil {
		return 0, fmt.Errorf("resolve Windows interface %q: %w", route.Interface, nameErr)
	}
	return 0, errors.New("Windows route interface is required")
}

func decodeWindowsRoute(output []byte) (windowsRouteRecord, error) {
	var record windowsRouteRecord
	if len(strings.TrimSpace(string(output))) == 0 {
		return record, ErrRouteNotFound
	}
	if err := json.Unmarshal(output, &record); err != nil {
		return record, fmt.Errorf("decode PowerShell route output: %w", err)
	}
	return record, nil
}

// decodeWindowsResolvedRoute 从 Find-NetRoute 的异构结果中分别提取路由和源地址。
func decodeWindowsResolvedRoute(output []byte) (ResolvedRoute, error) {
	if len(strings.TrimSpace(string(output))) == 0 {
		return ResolvedRoute{}, ErrRouteNotFound
	}
	var records []windowsRouteRecord
	if err := json.Unmarshal(output, &records); err != nil {
		return ResolvedRoute{}, fmt.Errorf("decode PowerShell resolved route output: %w", err)
	}
	var route *windowsRouteRecord
	sourceAddress := ""
	for index := range records {
		record := &records[index]
		if route == nil && record.DestinationPrefix != "" {
			route = record
		}
		if sourceAddress == "" && record.IPAddress != "" {
			sourceAddress = record.IPAddress
		}
	}
	if route == nil {
		return ResolvedRoute{}, ErrRouteNotFound
	}
	return ResolvedRoute{RouteSpec: route.RouteSpec(), SourceAddress: sourceAddress}, nil
}

func (r windowsRouteRecord) RouteSpec() RouteSpec {
	return RouteSpec{Prefix: r.DestinationPrefix, Gateway: r.NextHop, Interface: r.InterfaceAlias, InterfaceIndex: r.InterfaceIndex, Metric: r.RouteMetric}
}

func isPowerShellExit(err error, code int) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == code
}
