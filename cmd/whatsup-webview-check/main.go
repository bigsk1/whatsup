// whatsup-webview-check is a bounded developer smoke test, not an application
// bridge or general-purpose browser. It loads only one compiled-in local page.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"time"
)

const (
	probePassed       = 0
	probeNoDisplay    = 2
	probeLoadFailed   = 3
	probeTerminated   = 4
	probeJSFailed     = 5
	probeTimedOut     = 6
	probeUnsupported  = 7
	probeNativeFailed = 8
	probeUnsafeEnv    = 9
	probeUsage        = 64
	probeChildBase    = 32
)

func resultName(code int) string {
	switch code {
	case probePassed:
		return "passed"
	case probeNoDisplay:
		return "display-unavailable"
	case probeLoadFailed:
		return "local-page-load-failed"
	case probeTerminated:
		return "web-process-terminated"
	case probeJSFailed:
		return "javascript-check-failed"
	case probeTimedOut:
		return "timed-out"
	case probeUnsupported:
		return "unsupported-build"
	case probeUnsafeEnv:
		return "unsafe-sandbox-override"
	case probeUsage:
		return "invalid-options"
	default:
		return "native-process-failed"
	}
}

func unsafeSandboxOverride() bool {
	// WebKit's dangerous escape hatch is not a valid successful test. Do not
	// mutate it or claim a sandboxed result obtained with it enabled.
	_, present := os.LookupEnv("WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS")
	return present
}

func main() {
	options := flag.NewFlagSet("whatsup-webview-check", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	accelerated := options.Bool("accelerated", false, "test accelerated rendering instead of software rendering")
	child := options.Bool("native-child", false, "internal bounded native probe")
	code := probeUsage
	if options.Parse(os.Args[1:]) == nil && options.NArg() == 0 {
		if unsafeSandboxOverride() {
			code = probeUnsafeEnv
		} else if *child {
			// Keep native GLib aborts inside this exact same binary's child.
			// The parent emits only a bounded fixed result, not a crash dump.
			os.Exit(probeChildBase + runNativeProbe(*accelerated))
		} else {
			code = runBoundedChild(*accelerated)
		}
	}
	rendering := "software"
	if *accelerated {
		rendering = "accelerated"
	}
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		SchemaVersion int    `json:"schemaVersion"`
		Probe         string `json:"probe"`
		Rendering     string `json:"rendering"`
		Status        string `json:"status"`
		ExitCode      int    `json:"exitCode"`
	}{1, "webkit-native-runtime", rendering, resultName(code), code})
	os.Exit(code)
}

func runBoundedChild(accelerated bool) int {
	executable, err := os.Executable()
	if err != nil {
		return probeNativeFailed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	args := []string{"--native-child"}
	if accelerated {
		args = append(args, "--accelerated")
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	prepareNativeCommand(command)
	if err := command.Run(); err == nil {
		return probeNativeFailed
	} else if ctx.Err() != nil {
		return probeTimedOut
	} else {
		var exited *exec.ExitError
		if errors.As(err, &exited) {
			return decodeChildExit(exited.ExitCode())
		}
	}
	return probeNativeFailed
}

func decodeChildExit(status int) int {
	// Native Go/GLib fatal exits (notably status 2) must not be
	// misreported as a clean no-display result from our C function.
	code := status - probeChildBase
	if code == probePassed || (code >= probeNoDisplay && code <= probeUnsafeEnv) {
		return code
	}
	return probeNativeFailed
}
