package main

import (
	"embed"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"whatsup/internal/desktop"
	"whatsup/internal/diagnostics"
	"whatsup/internal/launch"
	"whatsup/internal/messaging"
	"whatsup/internal/storage"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var trayIcon []byte

func main() {
	options, err := launch.Parse(os.Args[1:], runtime.GOOS)
	if err != nil {
		log.Fatal("WhatsUp received unsupported launch arguments")
	}
	directory, err := storage.DefaultDirectory()
	if err != nil {
		log.Fatal("WhatsUp cannot locate a private local state directory")
	}
	var recorder atomic.Pointer[diagnostics.Recorder]
	record := func(event diagnostics.Event) {
		if current := recorder.Load(); current != nil {
			if err := current.Record(event); err != nil {
				log.Print("WhatsUp could not write a local diagnostic event")
			}
		}
	}
	closeRecorder := func() {
		if current := recorder.Load(); current != nil {
			_ = current.Close()
		}
	}
	windows := application.WindowsOptions{DisableQuitOnLastWindowClosed: true}
	if runtime.GOOS == "windows" {
		windows.WebviewUserDataPath = filepath.Join(directory, "webview")
		// Preserve the pinned Wails default for this bundled-content renderer.
		windows.DisabledFeatures = []string{"msSmartScreenProtection"}
	}
	application.RegisterEvent[messaging.Hint](desktop.ContactChangedEvent)
	application.RegisterEvent[NativeStorageHint](nativeStorageChangedEvent)
	application.RegisterEvent[bool](nativeWindowVisibleEvent)
	var native atomic.Pointer[nativeHost]
	var backend atomic.Pointer[desktop.Service]
	var focusPending atomic.Bool
	app := application.New(application.Options{
		Name: "WhatsUp", Description: "Private one-to-one desktop messenger",
		Windows: windows,
		Linux:   application.LinuxOptions{ProgramName: desktop.LinuxProgramName, DisableQuitOnLastWindowClosed: true},
		SingleInstance: &application.SingleInstanceOptions{UniqueID: "org.whatsup.desktop", ExitCode: 0, OnSecondInstanceLaunch: func(_ application.SecondInstanceData) {
			// Second-launch data is never interpreted as an invite, path, URL or
			// command. A launch can request only that the existing window be shown.
			focusPending.Store(true)
			if host := native.Load(); host != nil && focusPending.Swap(false) {
				host.focus()
			}
		}},
		Logger:         diagnostics.LoggerFor(record),
		ErrorHandler:   func(error) { record(diagnostics.RuntimeError) },
		WarningHandler: func(string) { record(diagnostics.RuntimeWarning) },
		PanicHandler: func(*application.PanicDetails) {
			record(diagnostics.RuntimePanic)
			closeRecorder()
			log.Fatal("WhatsUp stopped after a runtime error. Local diagnostics contain a redacted event.")
		},
		OnShutdown: func() {
			if host := native.Load(); host != nil {
				host.stopTasks()
			}
		},
		PostShutdown: func() { record(diagnostics.Shutdown); closeRecorder() },
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets), DisableLogging: true,
			Middleware: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("X-Content-Type-Options", "nosniff")
					w.Header().Set("Referrer-Policy", "no-referrer")
					if desktop.ServeAttachment(backend.Load(), w, r) {
						return
					}
					next.ServeHTTP(w, r)
				})
			},
		},
	})
	// application.New performs single-instance handoff before any primary-only
	// private file lock or OS credential is acquired.
	localDiagnostics, err := diagnostics.Open(directory, desktop.Version)
	if err != nil {
		log.Fatal("WhatsUp cannot open private local diagnostics")
	}
	recorder.Store(localDiagnostics)
	defer closeRecorder()
	record(diagnostics.Startup)
	if runtime.GOOS == "linux" {
		if _, err := desktop.PrepareLinuxWebviewProfile(directory); err != nil {
			record(diagnostics.RuntimeError)
			log.Fatal("WhatsUp cannot verify its private WebKit data and cache profile")
		}
	} else if runtime.GOOS == "windows" {
		if _, err := desktop.PrepareWebviewProfile(directory); err != nil {
			record(diagnostics.RuntimeError)
			log.Fatal("WhatsUp cannot create its private WebView profile")
		}
	}
	service := desktop.NewWithEvents(directory, storage.OSKeyStore{}, record, func(hint messaging.Hint) { app.Event.Emit(desktop.ContactChangedEvent, hint) })
	backend.Store(service)
	host, err := newNativeHost(app, service, record, options, trayIcon)
	if err != nil {
		record(diagnostics.RuntimeError)
		log.Fatal("WhatsUp could not initialize its desktop controls")
	}
	if err := desktop.ConfigureRuntime(service, desktop.RuntimeOptions{AutoConnect: true, OnIncoming: host.enqueueIncoming}); err != nil {
		log.Fatal("WhatsUp could not configure its desktop runtime")
	}
	if err := desktop.ConfigureAppPreferences(service, desktop.AppPreferenceRuntimeOptions{SetStartAtLogin: host.setStartAtLogin, OnChanged: host.preferencesChanged}); err != nil {
		record(diagnostics.RuntimeWarning)
	}
	if err := host.configureSecurity(); err != nil {
		log.Fatal("WhatsUp could not initialize local recovery controls")
	}
	if err := host.configureFiles(); err != nil {
		log.Fatal("WhatsUp could not initialize private file input")
	}
	native.Store(host)
	if focusPending.Swap(false) {
		host.focus()
	}
	app.RegisterService(application.NewService(service))
	app.RegisterService(application.NewService(&nativeLifecycle{host: host}))
	linuxWindow := application.LinuxWindow{}
	if options.SoftwareRendering {
		linuxWindow.WebviewGpuPolicy = application.WebviewGpuPolicyNever
	}
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "WhatsUp", Width: 1180, Height: 780, MinWidth: 760, MinHeight: 560,
		BackgroundColour: application.NewRGB(12, 20, 23), URL: "/",
		DevToolsEnabled: false, DefaultContextMenuDisabled: true,
		NativeFileDropHandler: host.nativeFileDrop,
		Linux:                 linuxWindow,
	})
	host.attachWindow(window)
	if err := app.Run(); err != nil {
		record(diagnostics.RuntimeError)
		host.stopTasks()
		_ = service.ServiceShutdown()
		closeRecorder()
		log.Fatal("WhatsUp desktop runtime failed")
	}
}
