//go:build !linux

package desktop

import "os"

// Windows uses its per-user application-data directory and inherited ACLs.
// Unix owner/mode bits cannot establish Windows ACL isolation.
func profileDirectoryOwnedByCurrentUser(os.FileInfo) bool { return true }
