//go:build darwin || linux

package introspection

import "syscall"

// diskFree reports free and total bytes on the filesystem holding path.
// The uint64 conversions normalize the platform difference: Bsize is
// uint32 on darwin and int64 on linux, so one file compiles on both.
func diskFree(path string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize)
	return uint64(st.Bavail) * bsize, uint64(st.Blocks) * bsize, nil
}
