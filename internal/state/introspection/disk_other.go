//go:build !darwin && !linux

package introspection

import "errors"

// diskFree is unsupported off darwin/linux; the host snapshot simply
// omits disk numbers there.
func diskFree(string) (free, total uint64, err error) {
	return 0, 0, errors.New("disk stats unsupported on this platform")
}
