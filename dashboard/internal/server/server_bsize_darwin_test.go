//go:build darwin

package server_test

// bsizeField converts a test's logical "bytes per block" value to the
// platform-specific type of unix.Statfs_t.Bsize. On Darwin it is uint32.
// Mirrors the helper in internal/disk so the disk fakes wired through
// server.New(...) compile cleanly on both Linux and Darwin CI hosts.
func bsizeField(n uint64) uint32 { return uint32(n) }
