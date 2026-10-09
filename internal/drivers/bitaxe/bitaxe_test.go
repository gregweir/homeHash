package bitaxe

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gregweir/homeHash/internal/drivers/base"
)

// Fixture: real response from a Bitaxe Gamma running firmware v2.13.0,
// trimmed to the fields homeHash parses.
const sysInfoRealGamma = `{
	"hostname": "Gamma 601",
	"hashRate": 1191.4256592,
	"expectedHashrate": 1264.8000488,
	"temp": 54.125,
	"temp2": -1,
	"vrTemp": 67,
	"fanspeed": 80,
	"fanrpm": 8667,
	"sharesAccepted": 102983,
	"sharesRejected": 57,
	"uptimeSeconds": 3798833,
	"stratumURL": "192.168.1.45",
	"stratumPort": 2018,
	"stratumUser": "bc1qxwxscay9038hsjgrsnqsk8f395a903hxlg4xwu.Gamma601",
	"power": 20.8627319,
	"voltage": 5242.1875,
	"frequency": 620,
	"macAddr": "AA:BB:CC:DD:EE:FF",
	"version": "v2.13.0"
}`

func minerAt(ip string) base.Miner {
	return base.Miner{ID: "test-miner", IP: ip, Driver: "bitaxe"}
}

func newFakeMiner(t *testing.T, body string) (d *BitaxeDriver, ip string, srv *httptest.Server) {
	t.Helper()
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	d = New()
	// Point the driver at the test server instead of a real miner IP.
	srvIP := srv.Listener.Addr().String()
	ip = srvIP
	return d, ip, srv
}

func TestPollParsesRealSystemInfo(t *testing.T) {
	d, ip, _ := newFakeMiner(t, sysInfoRealGamma)

	stats, err := d.Poll(minerAt(ip))
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}

	if stats.HashRateGH != 1191.4256592 {
		t.Errorf("HashRateGH: want 1191.4256592 (GH/s direct), got %v", stats.HashRateGH)
	}
	if stats.TemperatureC != 54.125 {
		t.Errorf("TemperatureC: want 54.125, got %v", stats.TemperatureC)
	}
	if stats.FanSpeed != 80 {
		t.Errorf("FanSpeed: want 80, got %d", stats.FanSpeed)
	}
	if stats.Accepted != 102983 || stats.Rejected != 57 {
		t.Errorf("shares: want 102983/57, got %d/%d", stats.Accepted, stats.Rejected)
	}
	if stats.Uptime != 3798833*time.Second {
		t.Errorf("Uptime: want 3798833s, got %v", stats.Uptime)
	}
	if stats.PowerW < 0.0001 || diff(stats.PowerW, 20.8627319) > 0.01 {
		t.Errorf("PowerW: want 20.8627319, got %v", stats.PowerW)
	}
}

func diff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

func TestUpdateInfoEnrichesMinerIdentity(t *testing.T) {
	d, ip, _ := newFakeMiner(t, sysInfoRealGamma)

	m := minerAt(ip)
	got, err := d.UpdateInfo(m)
	if err != nil {
		t.Fatalf("UpdateInfo: %v", err)
	}
	if got.Model != "Gamma 601" {
		t.Errorf("Model: want \"Gamma 601\", got %q", got.Model)
	}
	if got.Firmware != "v2.13.0" {
		t.Errorf("Firmware: want \"v2.13.0\", got %q", got.Firmware)
	}
	if got.PoolURL != "stratum+tcp://192.168.1.45:2018" {
		t.Errorf("PoolURL: want stratum+tcp://192.168.1.45:2018, got %q", got.PoolURL)
	}
	if got.MAC != "AA:BB:CC:DD:EE:FF" {
		t.Errorf("MAC: want AA:BB:CC:DD:EE:FF, got %q", got.MAC)
	}
}

func TestPollErrorsOnBadJSON(t *testing.T) {
	d, ip, _ := newFakeMiner(t, `not json`)
	if _, err := d.Poll(minerAt(ip)); err == nil {
		t.Error("want error for invalid JSON response, got nil")
	}
}

// Older firmware can omit "power" and report only voltage (mV) and current
// (mA); the driver must fall back to V×I.
const sysInfoNoPowerField = `{
	"hostname": "GammaTurbo 620",
	"hashRate": 800,
	"temp": 55.5,
	"fanspeed": 65,
	"sharesAccepted": 10,
	"sharesRejected": 0,
	"uptimeSeconds": 100,
	"voltage": 5300,
	"current": 14000,
	"version": "v2.12.0"
}`

func TestPollParsesPowerFromVoltageAndCurrentFallback(t *testing.T) {
	d, ip, _ := newFakeMiner(t, sysInfoNoPowerField)

	stats, err := d.Poll(minerAt(ip))
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	// mV × mA / 1e6 = W → 5.3 V × 14.0 A = 74.2 W
	if diff(stats.PowerW, 74.2) > 0.02 {
		t.Errorf("PowerW: want ~74.2, got %v", stats.PowerW)
	}
}
