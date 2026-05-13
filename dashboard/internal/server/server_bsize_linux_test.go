//go:build linux

package server_test

// bsizeField converts a test's logical "bytes per block" value to the
// platform-specific type of unix.Statfs_t.Bsize. On Linux it is int64.
// Mirrors the helper in internal/disk so the disk fakes wired through
// server.New(...) compile cleanly on both Linux and Darwin CI hosts.
func bsizeField(n uint64) int64 { return int64(n) }
