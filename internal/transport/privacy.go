package transport

import (
	"tailscale.com/envknob"
	"tailscale.com/logtail"
)

func init() {
	// Supported process controls complement omission tags in the shared workflow,
	// including for developers invoking a plain go test/build outside that runner.
	// They do not affect WhatsUp's independent bounded local diagnostic recorder.
	envknob.SetNoLogsNoSupport()
	logtail.Disable()
}
