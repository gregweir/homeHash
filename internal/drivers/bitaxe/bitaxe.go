// Package bitaxe implements the MinerDriver interface for Bitaxe miners.
// Tested hardware: Bitaxe Gamma (BM1370, firmware v2.13.0),
// Bitaxe GammaTurbo (BM1370, firmware v2.12.0), 2026-10.
//
// axeOS exposes a REST API on port 80. In the firmware versions above,
// all live data comes from a single endpoint:
//
//	GET /api/system/info -> hash rate, temp, shares, power, fan, uptime,
//	                        pool config, firmware, hostname, MAC
//
// (The older separate /api/stats endpoint returns "unknown route" on these
// firmwares.)
//
// Unit notes, verified against hardware:
//   - hashRate is in GH/s directly (1191.4 for a ~1.2 TH miner)
//   - power is in watts; voltage/current are in mV/mA
//   - shares are sharesAccepted / sharesRejected (NOT accepted/rejected)
//   - fanspeed is lowercase and is a percentage; fanrpm is RPM
package bitaxe

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gregweir/homeHash/internal/drivers/base"
)

const (
	driverName = "bitaxe"
	apiTimeout = 5 * time.Second
)

// BitaxeDriver implements base.MinerDriver for Bitaxe hardware.
type BitaxeDriver struct {
	client *http.Client
}

// compile-time checks that the driver supports identity updates and control.
var (
	_ base.InfoUpdater    = (*BitaxeDriver)(nil)
	_ base.ControlUpdater = (*BitaxeDriver)(nil)
)

// New creates a new BitaxeDriver with sensible defaults.
func New() *BitaxeDriver {
	return &BitaxeDriver{
		client: &http.Client{Timeout: apiTimeout},
	}
}

// Name returns the driver identifier.
func (d *BitaxeDriver) Name() string {
	return driverName
}

// SupportedModels lists the Bitaxe models this driver handles.
func (d *BitaxeDriver) SupportedModels() []string {
	return []string{"Bitaxe Gamma", "Bitaxe GammaTurbo", "Bitaxe Ultra", "Bitaxe Max"}
}

// Discover finds Bitaxe miners on the local network.
// First tries mDNS/Bonjour, then falls back to IP range scan.
func (d *BitaxeDriver) Discover() ([]base.Miner, error) {
	// TODO: implement mDNS discovery
	// TODO: implement IP range fallback
	// Miners are seeded manually (HOMEHASH_MINER) until this is built.
	return []base.Miner{}, nil
}

// Poll collects current statistics from a specific Bitaxe miner.
func (d *BitaxeDriver) Poll(miner base.Miner) (base.Stats, error) {
	info, err := d.fetchJSON(miner.IP)
	if err != nil {
		return base.Stats{}, err
	}
	return parseInfo(info), nil
}

// UpdateInfo refreshes miner identity fields from the device itself.
func (d *BitaxeDriver) UpdateInfo(miner base.Miner) (base.Miner, error) {
	info, err := d.fetchJSON(miner.IP)
	if err != nil {
		return miner, err
	}

	updated := miner
	if h, ok := info["hostname"].(string); ok && h != "" {
		updated.Model = h
	}
	if v, ok := info["version"].(string); ok && v != "" {
		updated.Firmware = v
	}
	if url, port, ok := poolFrom(info); ok {
		updated.PoolURL = fmt.Sprintf("stratum+tcp://%s:%d", url, port)
	}
	if mac, ok := info["macAddr"].(string); ok && mac != "" {
		updated.MAC = mac
	}
	return updated, nil
}

// poolFrom reads the stratum config, tolerating float-typed ports.
func poolFrom(info map[string]interface{}) (url string, port int, ok bool) {
	u, ok := info["stratumURL"].(string)
	if !ok || u == "" {
		return "", 0, false
	}
	p, ok := info["stratumPort"].(float64)
	if !ok {
		return "", 0, false
	}
	return u, int(p), true
}

// fetchJSON performs a GET request and returns the parsed JSON object.
func (d *BitaxeDriver) fetchJSON(ip string) (map[string]interface{}, error) {
	url := fmt.Sprintf("http://%s/api/system/info", ip)
	resp, err := d.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch system info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode system info: %w", err)
	}
	return data, nil
}

// num extracts a numeric field, tolerating null (JSON null unmarshals to nil
// interface, which fails the type assertion).
func num(info map[string]interface{}, key string) (float64, bool) {
	v, ok := info[key].(float64)
	return v, ok
}

// parseInfo converts a /api/system/info response into unified Stats.
func parseInfo(info map[string]interface{}) base.Stats {
	s := base.Stats{Timestamp: time.Now()}

	// hashRate is GH/s directly on the tested firmware versions.
	if hr, ok := num(info, "hashRate"); ok {
		s.HashRateGH = hr
	}
	if temp, ok := num(info, "temp"); ok && temp >= 0 {
		s.TemperatureC = temp
	}
	if fan, ok := num(info, "fanspeed"); ok {
		s.FanSpeed = int(fan)
	}
	if acc, ok := num(info, "sharesAccepted"); ok {
		s.Accepted = int64(acc)
	}
	if rej, ok := num(info, "sharesRejected"); ok {
		s.Rejected = int64(rej)
	}
	if up, ok := num(info, "uptimeSeconds"); ok {
		s.Uptime = time.Duration(up) * time.Second
	}
	if pw, ok := num(info, "power"); ok {
		s.PowerW = pw
	} else {
		// Fallback: calculate from voltage (mV) and current (mA)
		if v, ok := num(info, "voltage"); ok {
			if c, ok := num(info, "current"); ok {
				s.PowerW = (v * c) / 1e6 // mV * mA / 1e6 = W
			}
		}
	}
	// Frequency/core voltage: current device settings (v2.15.3 keeps them
	// top-level; absent on firmware that doesn't report them → zero).
	if f, ok := num(info, "frequency"); ok {
		s.Frequency = f
	}
	if v, ok := num(info, "coreVoltage"); ok {
		s.CoreVoltageMv = int(v)
	}
	// What the ASIC actually runs vs what's configured (v2.15.3+).
	if f, ok := num(info, "actualFrequency"); ok {
		s.ActualFrequency = f
	}
	if v, ok := num(info, "coreVoltageActual"); ok {
		s.CoreVoltageActualMv = int(v)
	}
	// Older firmware has no miningPaused key; only trust an explicit true.
	s.Paused = info["miningPaused"] == true
	return s
}
