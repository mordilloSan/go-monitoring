//go:build !linux

package agent

import (
	"context"
	"errors"
)

func (gm *GPUManager) hasAmdSysfs() bool {
	return false
}

func (gm *GPUManager) collectAmdStats(_ context.Context) error {
	return errors.ErrUnsupported
}
