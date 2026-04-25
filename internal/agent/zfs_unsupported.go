//go:build !linux && !freebsd

package agent

import "errors"

func ARCSize() (uint64, error) {
	return 0, errors.ErrUnsupported
}
