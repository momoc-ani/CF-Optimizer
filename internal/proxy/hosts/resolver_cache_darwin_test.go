//go:build darwin

package hosts

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRefreshDarwinResolverCacheRunsRequiredCommands(t *testing.T) {
	var commands []string
	err := refreshDarwinResolverCache(context.Background(), func(_ context.Context, path string, args ...string) error {
		commands = append(commands, path+" "+strings.Join(args, " "))
		return nil
	})
	if err != nil {
		t.Fatalf("refresh resolver cache: %v", err)
	}
	want := []string{
		darwinDSCacheUtilPath + " -flushcache",
		darwinKillallPath + " -HUP mDNSResponder",
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("resolver cache commands = %#v, want %#v", commands, want)
	}
}

func TestRefreshDarwinResolverCacheStopsAfterFailure(t *testing.T) {
	wantErr := errors.New("flush failed")
	calls := 0
	err := refreshDarwinResolverCache(context.Background(), func(context.Context, string, ...string) error {
		calls++
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("refresh error = %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Fatalf("resolver cache command calls = %d, want 1", calls)
	}
}
