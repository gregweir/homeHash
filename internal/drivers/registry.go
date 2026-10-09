// Package registry manages all available miner drivers.
// New drivers are registered here and discovered at runtime.
package drivers

import (
	"github.com/gregweir/homeHash/internal/drivers/base"
	"github.com/gregweir/homeHash/internal/drivers/bitaxe"
)

// Registry holds all registered miner drivers.
type Registry struct {
	drivers map[string]base.MinerDriver
}

// New creates a new Registry with all built-in drivers registered.
func New() *Registry {
	r := &Registry{
		drivers: make(map[string]base.MinerDriver),
	}

	// Register built-in drivers
	r.Register(bitaxe.New())

	return r
}

// Register adds a driver to the registry.
func (r *Registry) Register(driver base.MinerDriver) {
	r.drivers[driver.Name()] = driver
}

// Get retrieves a driver by name.
func (r *Registry) Get(name string) (base.MinerDriver, bool) {
	d, ok := r.drivers[name]
	return d, ok
}

// All returns all registered drivers.
func (r *Registry) All() []base.MinerDriver {
	result := make([]base.MinerDriver, 0, len(r.drivers))
	for _, d := range r.drivers {
		result = append(result, d)
	}
	return result
}
