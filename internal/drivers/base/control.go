package base

// SettingsPatch describes a partial settings change to a miner. Nil fields
// are left untouched; drivers must send only the provided keys to the
// device, so an incomplete patch can never zero out the others.
type SettingsPatch struct {
	Frequency   *float64 // ASIC frequency, MHz
	CoreVoltage *int     // core voltage, mV
}

// ControlUpdater is optionally implemented by drivers whose miners can
// change runtime settings, or pause and resume mining. The API exposes these
// as write endpoints gated behind HOMEHASH_CONTROL.
type ControlUpdater interface {
	// ApplySettings applies a partial settings patch.
	ApplySettings(miner Miner, patch SettingsPatch) error

	// Pause stops mining work (monitoring keeps working; the device keeps
	// responding to its API).
	Pause(miner Miner) error

	// Resume resumes mining.
	Resume(miner Miner) error
}
