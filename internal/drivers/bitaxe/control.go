// Control support for the Bitaxe driver: axeOS accepts a partial settings
// update on PATCH /api/system (only provided keys touch their values; the
// firmware applies frequency/voltage live, no restart needed), and mining
// pause/resume on POST /api/system/pause|resume. Both exist on v2.12.0 and
// v2.15.3 with the same keys.
package bitaxe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gregweir/homeHash/internal/drivers/base"
)

// ApplySettings applies a partial settings patch to the miner.
func (d *BitaxeDriver) ApplySettings(miner base.Miner, patch base.SettingsPatch) error {
	if patch.Frequency == nil && patch.CoreVoltage == nil {
		return nil // nothing to change, and an empty patch must not be sent
	}

	body := map[string]interface{}{}
	if patch.Frequency != nil {
		body["frequency"] = *patch.Frequency
	}
	if patch.CoreVoltage != nil {
		body["coreVoltage"] = *patch.CoreVoltage
	}
	return d.postJSON(miner.IP, http.MethodPatch, "/api/system", body)
}

// Pause stops mining; the device keeps serving stats.
func (d *BitaxeDriver) Pause(miner base.Miner) error {
	return d.postJSON(miner.IP, http.MethodPost, "/api/system/pause", nil)
}

// Resume resumes mining.
func (d *BitaxeDriver) Resume(miner base.Miner) error {
	return d.postJSON(miner.IP, http.MethodPost, "/api/system/resume", nil)
}

// postJSON sends a JSON body (or empty body for nil) and treats any
// non-2xx response as an error, surfacing the device's message when it
// gives one.
func (d *BitaxeDriver) postJSON(ip, method, path string, body map[string]interface{}) error {
	var payload io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s body: %w", path, err)
		}
		payload = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, "http://"+ip+path, payload)
	if err != nil {
		return fmt.Errorf("build %s request: %w", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: device returned HTTP %d", method, path, resp.StatusCode)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
	return nil
}
