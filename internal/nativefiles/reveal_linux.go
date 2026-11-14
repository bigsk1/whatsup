//go:build linux

package nativefiles

import (
	"context"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

func showInFolder(ctx context.Context, filename string) error {
	address := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if !strings.HasPrefix(address, "unix:") || strings.Contains(address, ";") {
		return ErrUnavailable
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := dbus.Dial(address, dbus.WithContext(call))
	if err != nil {
		return ErrUnavailable
	}
	defer conn.Close()
	stop := context.AfterFunc(call, func() { _ = conn.Close() })
	defer stop()
	if conn.Auth(nil) != nil || conn.Hello() != nil {
		return ErrUnavailable
	}
	uri := (&url.URL{Scheme: "file", Path: filename}).String()
	if conn.Object("org.freedesktop.FileManager1", "/org/freedesktop/FileManager1").CallWithContext(call, "org.freedesktop.FileManager1.ShowItems", 0, []string{uri}, "").Err != nil {
		return ErrUnavailable
	}
	return nil
}
