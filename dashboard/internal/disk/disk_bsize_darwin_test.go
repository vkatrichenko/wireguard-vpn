//go:build darwin

package disk

// bsizeField converts a test's logical "bytes per block" value to the
// platform-specific type of unix.Statfs_t.Bsize. On Darwin it is uint32.
func bsizeField(n uint64) uint32 { return uint32(n) }
