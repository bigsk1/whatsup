//go:build !windows && !linux

package launch

func newStartup(string, bool) (Entry, error) { return nil, ErrStartup }
