// Package main exposes only nFPM's Debian writer to the frozen build workflow.
// It deliberately does not include package readers, other formats or signers.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/goreleaser/nfpm/v2"
	_ "github.com/goreleaser/nfpm/v2/deb"
)

func main() {
	configPath := flag.String("config", "", "reviewed nFPM configuration")
	target := flag.String("target", "", "local Debian output")
	showVersion := flag.Bool("version", false, "show pinned nFPM version")
	flag.Parse()
	if *showVersion {
		fmt.Println("nfpm 2.49.0 (WhatsUp Debian writer)")
		return
	}
	if *configPath == "" || *target == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "use --config and --target")
		os.Exit(2)
	}
	config, err := nfpm.ParseFile(*configPath)
	if err != nil {
		fail(err)
	}
	info, err := config.Get("deb")
	if err != nil {
		fail(err)
	}
	packager, err := nfpm.Get("deb")
	if err != nil {
		fail(err)
	}
	output, err := os.OpenFile(*target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		fail(err)
	}
	err = packager.Package(info, output)
	closeErr := output.Close()
	if err != nil {
		fail(err)
	}
	if closeErr != nil {
		fail(closeErr)
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
