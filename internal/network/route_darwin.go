//go:build darwin

package network

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type darwinRouteBackend struct{ timeout time.Duration }

func newPlatformRouteBackend(commandTimeout time.Duration) RouteBackend {
	return &darwinRouteBackend{timeout: commandTimeout}
}

func (b *darwinRouteBackend) Replace(ctx context.Context, route RouteSpec) error {
	_ = b.Delete(ctx, route)
	prefix := netip.MustParsePrefix(route.Prefix)
	family := "-inet6"
	if prefix.Addr().Is4() {
		family = "-inet"
	}
	arguments := []string{"-n", "add", family, "-net", route.Prefix, route.Gateway}
	if route.Interface != "" {
		arguments = append(arguments, "-ifscope", route.Interface)
	}
	_, err := b.run(ctx, "route", arguments...)
	return err
}

func (b *darwinRouteBackend) Delete(ctx context.Context, route RouteSpec) error {
	prefix := netip.MustParsePrefix(route.Prefix)
	family := "-inet6"
	if prefix.Addr().Is4() {
		family = "-inet"
	}
	_, err := b.run(ctx, "route", "-n", "delete", family, "-net", route.Prefix)
	if err != nil && (strings.Contains(err.Error(), "not in table") || strings.Contains(err.Error(), "No such process")) {
		return ErrRouteNotFound
	}
	return err
}

func (b *darwinRouteBackend) Get(ctx context.Context, prefix string) (RouteSpec, error) {
	parsed := netip.MustParsePrefix(prefix)
	resolved, err := b.resolveText(ctx, parsed.Addr())
	if err != nil {
		return RouteSpec{}, err
	}
	if !darwinDestinationMatches(resolved.Prefix, parsed) {
		return RouteSpec{}, ErrRouteNotFound
	}
	resolved.Prefix = parsed.String()
	return resolved.RouteSpec, nil
}

func (b *darwinRouteBackend) Resolve(ctx context.Context, target netip.Addr) (ResolvedRoute, error) {
	return b.resolveText(ctx, target)
}

func (b *darwinRouteBackend) resolveText(ctx context.Context, target netip.Addr) (ResolvedRoute, error) {
	output, err := b.run(ctx, "route", "-n", "get", target.String())
	if err != nil {
		return ResolvedRoute{}, err
	}
	fields := parseDarwinRouteFields(string(output))
	destination := fields["destination"]
	if destination == "default" {
		if target.Is4() {
			destination = "0.0.0.0/0"
		} else {
			destination = "::/0"
		}
	} else if !strings.Contains(destination, "/") {
		bits := 128
		if target.Is4() {
			bits = 32
		}
		destination += "/" + strconv.Itoa(bits)
	}
	if _, err := netip.ParsePrefix(destination); err != nil {
		return ResolvedRoute{}, fmt.Errorf("parse macOS route destination %q: %w", destination, err)
	}
	return ResolvedRoute{
		RouteSpec:     RouteSpec{Prefix: destination, Gateway: fields["gateway"], Interface: fields["interface"]},
		SourceAddress: fields["source"],
	}, nil
}

func (b *darwinRouteBackend) run(ctx context.Context, executable string, arguments ...string) ([]byte, error) {
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

func parseDarwinRouteFields(output string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if found {
			fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return fields
}

func darwinDestinationMatches(actual string, expected netip.Prefix) bool {
	parsed, err := netip.ParsePrefix(actual)
	return err == nil && parsed.Masked() == expected.Masked()
}
