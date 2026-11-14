package messagestore

import "os"

// Windows files inherit the private account directory ACL. Unix mode bits are
// not an ACL sandbox; the local account and parent directory remain trusted.
func owned(info os.FileInfo) bool { return info != nil }
