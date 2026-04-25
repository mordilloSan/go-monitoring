package agent

const (
	stateUnknown uint8 = iota
	stateEmpty
	stateFull
	stateCharging
	stateDischarging
	stateIdle
)
