//go:build linux

package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type linuxRouteBackend struct{ timeout time.Duration }

type linuxRouteRecord struct {
	Destination string `json:"dst"`
	Gateway     string `json:"gateway"`
	Device      string `json:"dev"`
	Preferred   string `json:"prefsrc"`
	Metric      int    `json:"metric"`
}

func newPlatformRouteBackend(commandTimeout time.Duration) RouteBackend {
	return &linuxRouteBackend{timeout: commandTimeout}
}

func (b *linuxRouteBackend) Replace(ctx context.Context, route RouteSpec) error {
	family := "-6"
	if netip.MustParsePrefix(route.Prefix).Addr().Is4() {
		family = "-4"
	}
	arguments := []string{family, "route", "replace", route.Prefix, "via", route.Gateway, "dev", route.Interface, "proto", "static", "metric", strconv.Itoa(route.Metric)}
	_, err := b.run(ctx, "ip", arguments...)
	return err
}

func (b *linuxRouteBackend) Delete(ctx context.Context, route RouteSpec) error {
	family := "-6"
	if netip.MustParsePrefix(route.Prefix).Addr().Is4() {
		family = "-4"
	}
	_, err := b.run(ctx, "ip", family, "route", "del", route.Prefix)
	if err != nil && strings.Contains(err.Error(), "No such process") {
		return ErrRouteNotFound
	}
	return err
}

func (b *linuxRouteBackend) Get(ctx context.Context, prefix string) (RouteSpec, error) {
	family := "-6"
	if netip.MustParsePrefix(prefix).Addr().Is4() {
		family = "-4"
	}
	output, err := b.run(ctx, "ip", "-j", family, "route", "show", "exact", prefix)
	if err != nil {
		return RouteSpec{}, err
	}
	var records []linuxRouteRecord
	if err := json.Unmarshal(output, &records); err != nil {
		return RouteSpec{}, fmt.Errorf("decode ip route output: %w", err)
	}
	if len(records) == 0 {
		return RouteSpec{}, ErrRouteNotFound
	}
	return RouteSpec{Prefix: records[0].Destination, Gateway: records[0].Gateway, Interface: records[0].Device, Metric: records[0].Metric}, nil
}

func (b *linuxRouteBackend) Resolve(ctx context.Context, target netip.Addr) (ResolvedRoute, error) {
	family := "-6"
	if target.Is4() {
		family = "-4"
	}
	output, err := b.run(ctx, "ip", "-j", family, "route", "get", target.String())
	if err != nil {
		return ResolvedRoute{}, err
	}
	var records []linuxRouteRecord
	if err := json.Unmarshal(output, &records); err != nil {
		return ResolvedRoute{}, fmt.Errorf("decode ip route get output: %w", err)
	}
	if len(records) == 0 {
		return ResolvedRoute{}, ErrRouteNotFound
	}
	record := records[0]
	return ResolvedRoute{RouteSpec: RouteSpec{Prefix: record.Destination, Gateway: record.Gateway, Interface: record.Device, Metric: record.Metric}, SourceAddress: record.Preferred}, nil
}

func (b *linuxRouteBackend) run(ctx context.Context, executable string, arguments ...string) ([]byte, error) {
	commandContext, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	output, err := exec.CommandContext(commandContext, executable, arguments...).CombinedOutput()
	if err != nil {
		if errors.Is(commandContext.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s timed out: %w", executable, commandContext.Err())
		}
		return nil, fmt.Errorf("%s failed: %w: %s", executable, err, strings.TrimSpace(string(output)))
	}
	return output, nil
}
