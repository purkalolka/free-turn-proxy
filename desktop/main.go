package main

import (
	"context"
	"embed"

	"github.com/samosvalishe/free-turn-proxy/desktop/tray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:             "FreeTurn Desktop",
		Width:             1180,
		Height:            780,
		MinWidth:          900,
		MinHeight:         600,
		HideWindowOnClose: true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			t, terr := tray.Start(
				"FreeTurn Desktop",
				func() {
					wailsruntime.WindowShow(ctx)
					wailsruntime.WindowUnminimise(ctx)
				},
				func() {
					wailsruntime.Quit(ctx)
				},
			)
			if terr == nil && t != nil {
				app.SetTray(t)
			}
		},
		OnShutdown: app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
