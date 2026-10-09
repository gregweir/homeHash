// Package discovery finds homeHash miners on the local network: mDNS/DNS-SD
// first (esp-miner advertises _axeos._sub._http._tcp), IP range scan as a
// fallback, and every candidate verified over HTTP before it's claimed.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gregweir/homeHash/internal/drivers/base"
)

// Discoverer finds miners on the local network.
type Discoverer struct {
	methods []DiscoveryMethod
}

// DiscoveryMethod is a single discovery strategy (mDNS, IP scan, ...).
type DiscoveryMethod interface {
	Name() string
	Discover(ctx context.Context) ([]base.Miner, error)
}

// New creates a Discoverer with the production methods.
func New() *Discoverer {
	return &Discoverer{
		methods: []DiscoveryMethod{
			newMDNS(),
			newIPScan(),
		},
	}
}

// NewWithMethods creates a Discoverer from explicit methods (tests).
func NewWithMethods(methods []DiscoveryMethod) *Discoverer {
	return &Discoverer{methods: methods}
}

// Discover runs all discovery methods and returns a deduplicated list of miners.
func (d *Discoverer) Discover(ctx context.Context) ([]base.Miner, error) {
	var allMiners []base.Miner
	for _, method := range d.methods {
		miners, err := method.Discover(ctx)
		if err != nil {
			log.Printf("Discovery method %s failed: %v", method.Name(), err)
			continue
		}
		if len(miners) > 0 {
			log.Printf("Discovery method %s found %d miners", method.Name(), len(miners))
		}
		allMiners = append(allMiners, miners...)
	}
	return dedupe(allMiners), nil
}

// VerifyMiner checks if a specific address runs an axeOS web API. The
// address is "ip" or "ip:port" as handed to discovery candidates. A 200
// alone is not enough — routers and other web devices answer 200 too — so
// the body must decode as JSON carrying axeOS fields.
func VerifyMiner(addr string, timeout time.Duration) (bool, error) {
	client := &http.Client{Timeout: timeout}
	url := fmt.Sprintf("http://%s/api/system/info", addr)

	resp, err := client.Get(url)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false, nil
	}

	var info map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&info); err != nil {
		return false, nil // 200 HTML from some other device
	}
	// Any single axeOS signature field identifies the firmware.
	for _, key := range []string{"hashRate", "version", "asicCount", "stratumURL", "frequency"} {
		if _, ok := info[key]; ok {
			return true, nil
		}
	}
	return false, nil
}
