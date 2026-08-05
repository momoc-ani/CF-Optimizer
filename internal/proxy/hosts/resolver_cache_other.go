//go:build !darwin

package hosts

import "context"

// refreshResolverCache 在非 macOS 平台保持现有 Hosts 生效机制不变。
func refreshResolverCache(context.Context) error { return nil }
