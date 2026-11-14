package main

import (
	"embed"
	"log"
	"net/http"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
	"whatsup/internal/desktop"
	"whatsup/internal/diagnostics"
	"whatsup/internal/storage"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	directory, err := storage.DefaultDirectory()
	if err != nil {
		log.Fatal("WhatsUp cannot locate a private local state directory")
	}
	localDiagnostics, err := diagnostics.Open(directory, desktop.Version)
	if err != nil {
		log.Fatal("WhatsUp cannot open private local diagnostics. Check whether another instance is running.")
	}
	defer localDiagnostics.Close()
	record := func(event diagnostics.Event) {
		if err := localDiagnostics.Record(event); err != nil {
			log.Print("WhatsUp could not write a local diagnostic event")
		}
	}
	record(diagnostics.Startup)
	windows := application.WindowsOptions{}
	if runtime.GOOS == "linux" {
		if _, err := desktop.PrepareLinuxWebviewProfile(directory); err != nil {
			record(diagnostics.RuntimeError)
			log.Fatal("WhatsUp cannot verify its private WebKit data and cache profile")
		}
	}
	if runtime.GOOS == "windows" {
		profile, err := desktop.PrepareWebviewProfile(directory)
		if err != nil {
			record(diagnostics.RuntimeError)
			log.Fatal("WhatsUp cannot create its private WebView profile")
		}
		windows.WebviewUserDataPath = profile
		// Preserve the pinned Wails default for this bundled-content renderer.
		windows.DisabledFeatures = []string{"msSmartScreenProtection"}
	}
	app := application.New(application.Options{
		Name: "WhatsUp", Description: "Private one-to-one desktop messenger",
		Windows:        windows,
		Linux:          application.LinuxOptions{ProgramName: desktop.LinuxProgramName},
		Services:       []application.Service{application.NewService(desktop.New(directory, storage.OSKeyStore{}, record))},
		Logger:         localDiagnostics.WailsLogger(),
		ErrorHandler:   func(error) { record(diagnostics.RuntimeError) },
		WarningHandler: func(string) { record(diagnostics.RuntimeWarning) },
		PanicHandler: func(*application.PanicDetails) {
			record(diagnostics.RuntimePanic)
			_ = localDiagnostics.Close()
			log.Fatal("WhatsUp stopped after a runtime error. Local diagnostics contain a redacted event.")
		},
		PostShutdown: func() { record(diagnostics.Shutdown); _ = localDiagnostics.Close() },
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets), DisableLogging: true,
			Middleware: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("X-Content-Type-Options", "nosniff")
					w.Header().Set("Referrer-Policy", "no-referrer")
					next.ServeHTTP(w, r)
				})
			},
		},
	})
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "WhatsUp", Width: 1180, Height: 780, MinWidth: 760, MinHeight: 560,
		BackgroundColour: application.NewRGB(12, 20, 23), URL: "/",
		DevToolsEnabled: false, DefaultContextMenuDisabled: true,
	})
	if err := app.Run(); err != nil {
		record(diagnostics.RuntimeError)
		_ = localDiagnostics.Close()
		log.Fatal("WhatsUp desktop runtime failed")
	}
}
