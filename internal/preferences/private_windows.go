package preferences

import "os"

func owned(os.FileInfo) bool { return true }

// The ciphertext file is synced before replacement. Portable Go has no Windows
// directory fsync; this does not claim power-cut rollback guarantees.
func syncDirectory(string) error { return nil }
