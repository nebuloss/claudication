package recordings

import "syscall"

// Free is the space left for an unprivileged writer on the filesystem dir is
// on, and whether it could be read.
func Free(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}
