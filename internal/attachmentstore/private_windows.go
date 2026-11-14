package attachmentstore

import "os"

func owned(os.FileInfo) bool     { return true }
func syncDirectory(string) error { return nil }
