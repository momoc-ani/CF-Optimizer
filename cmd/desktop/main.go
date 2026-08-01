package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/cf-optimizer/cf-optimizer/desktop"
	"github.com/cf-optimizer/cf-optimizer/internal/config"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "cf-optimizer-ui:", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("cf-optimizer-ui", flag.ContinueOnError)
	endpoint := flags.String("endpoint", config.DefaultEndpoint(config.DefaultDataDir()), "daemon IPC endpoint")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	bridge, err := desktop.NewBridge(*endpoint)
	if err != nil {
		return err
	}
	tray, err := newTrayController()
	if err != nil {
		return err
	}
	tray.Register()
	defer tray.Shutdown(context.Background())
	return wails.Run(&options.App{
		Title:            "CF Optimizer",
		Width:            1280,
		Height:           820,
		MinWidth:         720,
		MinHeight:        620,
		AssetServer:      &assetserver.Options{Assets: desktop.Assets},
		BackgroundColour: &options.RGBA{R: 246, G: 247, B: 248, A: 1},
		OnStartup: func(ctx context.Context) {
			bridge.Startup(ctx)
			tray.Startup(ctx)
		},
		OnShutdown:        tray.Shutdown,
		HideWindowOnClose: true,
		Bind:              []any{bridge},
	})
}
