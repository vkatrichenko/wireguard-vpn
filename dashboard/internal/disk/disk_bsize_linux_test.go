//go:build linux

package disk

// bsizeField converts a test's logical "bytes per block" value to the
// platform-specific type of unix.Statfs_t.Bsize. On Linux it is int64.
func bsizeField(n uint64) int64 { return int64(n) }
