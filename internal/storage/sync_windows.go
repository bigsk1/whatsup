package storage

// Windows flushes the temporary file before replacement. Directory fsync is not
// portable here; power-loss durability requires native crash/power-cut QA.
func syncDirectory(string) error { return nil }
