//go:build !linux

package agent

func scanMdraidDevices() []*DeviceInfo {
	return nil
}

func (sm *SmartManager) collectMdraidHealth(_ *DeviceInfo) (bool, error) {
	return false, nil
}
