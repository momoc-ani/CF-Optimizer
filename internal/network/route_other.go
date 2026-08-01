//go:build !linux && !darwin && !windows

package network

import (
	"context"
	"fmt"
	"net/netip"
	"runtime"
	"time"
)

type unsupportedRouteBackend struct{}

func newPlatformRouteBackend(time.Duration) RouteBackend { return unsupportedRouteBackend{} }

func (unsupportedRouteBackend) Replace(context.Context, RouteSpec) error {
	return fmt.Errorf("route replacement is not supported on %s", runtime.GOOS)
}

func (unsupportedRouteBackend) Delete(context.Context, RouteSpec) error {
	return fmt.Errorf("route deletion is not supported on %s", runtime.GOOS)
}

func (unsupportedRouteBackend) Get(context.Context, string) (RouteSpec, error) {
	return RouteSpec{}, ErrRouteNotFound
}

func (unsupportedRouteBackend) Resolve(context.Context, netip.Addr) (ResolvedRoute, error) {
	return ResolvedRoute{}, fmt.Errorf("route lookup is not supported on %s", runtime.GOOS)
}
