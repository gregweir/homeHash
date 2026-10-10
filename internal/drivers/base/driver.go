// Package base defines the MinerDriver interface that all miner implementations must satisfy.
// This is the plugin architecture that allows homeHash to support multiple miner types.
package base

import "time"

// Miner represents a discovered mining device on the local network.
type Miner struct {
	ID       string    // Unique identifier (MAC-derived or UUID)
	IP       string    // Local IP address (may include port)
	Model    string    // Human-readable model name
	Driver   string    // Driver name: "bitaxe", "nerdminer", etc.
	PoolURL  string    // Currently configured pool URL
	Firmware string    // Firmware version string
	MAC      string    // Device MAC address, if reported
	LastSeen time.Time // Last successful poll timestamp
}

// Stats represents a single polling result from a miner.
type Stats struct {
	HashRateGH    float64       // Current hash rate in GH/s
	Accepted      int64         // Lifetime accepted shares
	Rejected      int64         // Lifetime rejected shares
	TemperatureC  float64       // Board temperature in Celsius
	FanSpeed      int           // Fan speed percentage (0-100)
	PowerW        float64       // Power draw in watts
	Uptime        time.Duration // Since miner booted
	Timestamp     time.Time     // When this poll was taken
	Frequency     float64       // Configured ASIC frequency (MHz), 0 if unknown
	CoreVoltageMv int           // Configured core voltage (mV), 0 if unknown
	Paused        bool          // True while mining is paused (device-reported)

	ActualFrequency     float64 // Frequency the ASIC is actually running (MHz), 0 if firmware doesn't report it
	CoreVoltageActualMv int     // Voltage the ASIC is actually running (mV), 0 if firmware doesn't report it
}

// InfoUpdater is optionally implemented by drivers that can refresh a
// miner's identity fields (model, firmware, pool). The poller calls it
// when present, alongside Poll.
type InfoUpdater interface {
	UpdateInfo(miner Miner) (Miner, error)
}

// MinerDriver is the interface that all miner implementations must satisfy.
// New drivers are registered in the registry and auto-discovered at runtime.
type MinerDriver interface {
	// Name returns the driver identifier, e.g. "bitaxe", "nerdminer".
	Name() string

	// Discover finds miners of this type on the local network.
	// Returns a slice of Miner structs with at minimum IP and Model populated.
	Discover() ([]Miner, error)

	// Poll collects current statistics from a specific miner.
	// The Miner parameter is one previously returned by Discover.
	Poll(miner Miner) (Stats, error)

	// SupportedModels returns a list of model strings this driver handles,
	// e.g. ["Bitaxe Gamma", "Bitaxe Ultra"].
	SupportedModels() []string
}
