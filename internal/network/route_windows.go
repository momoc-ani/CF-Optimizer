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
	command := fmt.Sprintf("$route=Find-NetRoute -RemoteIPAddress '%s' -ErrorAction Stop|Select-Object -First 1 DestinationPrefix,NextHop,InterfaceAlias,InterfaceIndex,RouteMetric,IPAddress; $route|ConvertTo-Json -Compress", target)
	output, err := b.runPowerShell(ctx, command)
	if err != nil {
		return ResolvedRoute{}, err
	}
	record, err := decodeWindowsRoute(output)
	if err != nil {
		return ResolvedRoute{}, err
	}
	return ResolvedRoute{RouteSpec: record.RouteSpec(), SourceAddress: record.IPAddress}, nil
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
	if route.InterfaceIndex > 0 {
		return route.InterfaceIndex, nil
	}
	iface, err := net.InterfaceByName(route.Interface)
	if err != nil {
		return 0, fmt.Errorf("resolve Windows interface: %w", err)
	}
	return iface.Index, nil
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

func (r windowsRouteRecord) RouteSpec() RouteSpec {
	return RouteSpec{Prefix: r.DestinationPrefix, Gateway: r.NextHop, Interface: r.InterfaceAlias, InterfaceIndex: r.InterfaceIndex, Metric: r.RouteMetric}
}

func isPowerShellExit(err error, code int) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == code
}
