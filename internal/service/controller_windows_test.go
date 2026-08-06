//go:build windows

package service

import (
	"context"
	"reflect"
	"testing"
)

func TestWindowsStartRepairsFailureRecoveryBeforeStarting(t *testing.T) {
	var commands [][]string
	controller := &windowsController{
		config: controllerConfig{timeout: 0},
		runSC: func(_ context.Context, arguments ...string) error {
			commands = append(commands, append([]string(nil), arguments...))
			return nil
		},
	}

	if err := controller.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"failure", windowsServiceName, "reset=", "86400", "actions=", "restart/10000/restart/30000/restart/60000"},
		{"failureflag", windowsServiceName, "1"},
		{"start", windowsServiceName},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("sc command sequence = %#v, want %#v", commands, want)
	}
}
