package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gregweir/homeHash/internal/config"
	"github.com/gregweir/homeHash/internal/drivers"
	"github.com/gregweir/homeHash/internal/drivers/base"
	"github.com/gregweir/homeHash/internal/store"
)

// controlConfig is a Config with write access on and the default clamps.
func controlConfig(enabled bool) *config.Config {
	return &config.Config{
		ControlEnabled: enabled,
		ControlFreqMin: 100,
		ControlFreqMax: 750,
		ControlVoltMin: 900,
		ControlVoltMax: 1300,
	}
}

// fakeControlDriver records control calls; it implements MinerDriver so it
// can be registered, and ControlUpdater so write endpoints dispatch to it.
type fakeControlDriver struct {
	fails error

	settings []base.SettingsPatch
	pauses   []base.Miner
	resumes  []base.Miner
}

func (f *fakeControlDriver) Name() string                          { return "fakectl" }
func (f *fakeControlDriver) Discover() ([]base.Miner, error)       { return nil, nil }
func (f *fakeControlDriver) Poll(m base.Miner) (base.Stats, error) { return base.Stats{}, nil }
func (f *fakeControlDriver) SupportedModels() []string             { return []string{"Fake"} }

func (f *fakeControlDriver) ApplySettings(m base.Miner, p base.SettingsPatch) error {
	if f.fails != nil {
		return f.fails
	}
	f.settings = append(f.settings, p)
	return nil
}
func (f *fakeControlDriver) Pause(m base.Miner) error {
	if f.fails != nil {
		return f.fails
	}
	f.pauses = append(f.pauses, m)
	return nil
}
func (f *fakeControlDriver) Resume(m base.Miner) error {
	if f.fails != nil {
		return f.fails
	}
	f.resumes = append(f.resumes, m)
	return nil
}

// fakePlainDriver implements only MinerDriver (no control support).
type fakePlainDriver struct{}

func (fakePlainDriver) Name() string                          { return "fakeplain" }
func (fakePlainDriver) Discover() ([]base.Miner, error)       { return nil, nil }
func (fakePlainDriver) Poll(m base.Miner) (base.Stats, error) { return base.Stats{}, nil }
func (fakePlainDriver) SupportedModels() []string             { return []string{"Plain"} }

func newControlStore(t *testing.T) (*store.Store, *fakeControlDriver, *drivers.Registry) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	fd := &fakeControlDriver{}
	reg := drivers.New()
	reg.Register(fd)
	reg.Register(fakePlainDriver{})

	for _, m := range []base.Miner{
		{ID: "ctl-miner", IP: "192.168.1.10", Driver: "fakectl", LastSeen: time.Now()},
		{ID: "plain-miner", IP: "192.168.1.11", Driver: "fakeplain", LastSeen: time.Now()},
	} {
		if err := s.UpsertMiner(m); err != nil {
			t.Fatalf("seed %s: %v", m.ID, err)
		}
	}
	return s, fd, reg
}

// postJSON posts a JSON body to the handler and returns the recorder.
func postJSON(t *testing.T, h *Handler, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestApplySettingsDispatchesToDriver(t *testing.T) {
	s, fd, reg := newControlStore(t)
	h := New(s, WithConfig(controlConfig(true)), WithRegistry(reg))

	rec := postJSON(t, h, "/api/miners/ctl-miner/settings",
		`{"frequency": 620.5, "core_voltage": 1150}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if len(fd.settings) != 1 {
		t.Fatalf("driver got %d ApplySettings calls, want 1", len(fd.settings))
	}
	p := fd.settings[0]
	if p.Frequency == nil || *p.Frequency != 620.5 {
		t.Errorf("frequency patch = %v, want 620.5", p.Frequency)
	}
	if p.CoreVoltage == nil || *p.CoreVoltage != 1150 {
		t.Errorf("voltage patch = %v, want 1150", p.CoreVoltage)
	}
}

func TestApplySettingsForbiddenWhenControlDisabled(t *testing.T) {
	s, fd, _ := newControlStore(t)

	// Default construction never enables control.
	rec := postJSON(t, New(s), "/api/miners/ctl-miner/settings", `{"frequency": 620}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("default handler: want 403, got %d", rec.Code)
	}

	h := New(s, WithConfig(controlConfig(false)))
	rec = postJSON(t, h, "/api/miners/ctl-miner/settings", `{"frequency": 620}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("control off: want 403, got %d", rec.Code)
	}
	if len(fd.settings) != 0 {
		t.Fatal("driver must not be called while control is off")
	}
}

func TestApplySettingsRejectsOutOfRangeValues(t *testing.T) {
	s, fd, reg := newControlStore(t)
	h := New(s, WithConfig(controlConfig(true)), WithRegistry(reg))

	for name, body := range map[string]string{
		"freq too high":  `{"frequency": 2000}`,
		"freq too low":   `{"frequency": 50}`,
		"volt too low":   `{"core_voltage": 700}`,
		"volt too high":  `{"core_voltage": 1500}`,
		"no fields":      `{}`,
		"garbage values": `{"frequency": "six hundred"}`,
	} {
		rec := postJSON(t, h, "/api/miners/ctl-miner/settings", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", name, rec.Code)
		}
	}
	if len(fd.settings) != 0 {
		t.Errorf("driver reached %d times on rejected patches", len(fd.settings))
	}
}

func TestControlUnknownMinerReturns404(t *testing.T) {
	s, _, reg := newControlStore(t)
	h := New(s, WithConfig(controlConfig(true)), WithRegistry(reg))

	for _, path := range []string{
		"/api/miners/no-such/settings",
		"/api/miners/no-such/pause",
		"/api/miners/no-such/resume",
	} {
		rec := postJSON(t, h, path, `{"frequency": 620}`)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: want 404, got %d", path, rec.Code)
		}
	}
}

func TestControlDriverWithoutSupportReturns501(t *testing.T) {
	s, _, reg := newControlStore(t)
	h := New(s, WithConfig(controlConfig(true)), WithRegistry(reg))

	rec := postJSON(t, h, "/api/miners/plain-miner/settings", `{"frequency": 620}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("settings: want 501, got %d", rec.Code)
	}
	rec = postJSON(t, h, "/api/miners/plain-miner/pause", "")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("pause: want 501, got %d", rec.Code)
	}
}

// Unknown drivers use the miner's driver name that no registry driver claims.
func TestControlUnknownDriverReturns501(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()
	if err := s.UpsertMiner(base.Miner{ID: "ghost", IP: "192.168.1.12", Driver: "ghost", LastSeen: time.Now()}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := New(s, WithConfig(controlConfig(true)), WithRegistry(drivers.New()))
	rec := postJSON(t, h, "/api/miners/ghost/settings", `{"frequency": 620}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("want 501 for an unknown driver, got %d", rec.Code)
	}
}

func TestControlDeviceErrorSurfacedAs502(t *testing.T) {
	s, fd, reg := newControlStore(t)
	fd.fails = errFake("device said no")
	h := New(s, WithConfig(controlConfig(true)), WithRegistry(reg))

	for _, path := range []string{
		"/api/miners/ctl-miner/settings",
		"/api/miners/ctl-miner/pause",
		"/api/miners/ctl-miner/resume",
	} {
		rec := postJSON(t, h, path, `{"frequency": 620}`)
		if rec.Code != http.StatusBadGateway {
			t.Errorf("%s: want 502, got %d", path, rec.Code)
		}
	}
}

func TestPauseAndResumeDispatchToDriver(t *testing.T) {
	s, fd, reg := newControlStore(t)
	h := New(s, WithConfig(controlConfig(true)), WithRegistry(reg))

	if rec := postJSON(t, h, "/api/miners/ctl-miner/pause", ""); rec.Code != http.StatusOK {
		t.Fatalf("pause: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := postJSON(t, h, "/api/miners/ctl-miner/resume", ""); rec.Code != http.StatusOK {
		t.Fatalf("resume: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(fd.pauses) != 1 || len(fd.resumes) != 1 {
		t.Fatalf("driver calls: %d pauses, %d resumes", len(fd.pauses), len(fd.resumes))
	}
}

// The pause/resume guards apply even with no body at all.
func TestPauseGuardedByControlSwitch(t *testing.T) {
	s, fd, _ := newControlStore(t)
	rec := postJSON(t, New(s), "/api/miners/ctl-miner/pause", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 with control off, got %d", rec.Code)
	}
	rec = postJSON(t, New(s), "/api/miners/ctl-miner/resume", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("resume: want 403 with control off, got %d", rec.Code)
	}
	if len(fd.pauses) != 0 || len(fd.resumes) != 0 {
		t.Fatal("driver reached while control is off")
	}
}

func errFake(msg string) error { return &fakeError{msg} }

type fakeError struct{ msg string }

func (e *fakeError) Error() string { return e.msg }

// The stats feed advertises whether control is on, so the UI can hide the
// settings controls when write access is off.
func TestStatsReportsControlEnabled(t *testing.T) {
	s, _, reg := newControlStore(t)

	var on, off map[string]interface{}
	if err := json.Unmarshal(statsBody(t, New(s, WithConfig(controlConfig(true)), WithRegistry(reg))), &on); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(statsBody(t, New(s, WithRegistry(reg))), &off); err != nil {
		t.Fatal(err)
	}
	if on["control_enabled"] != true {
		t.Errorf("control on: control_enabled = %v", on["control_enabled"])
	}
	if off["control_enabled"] != false {
		t.Errorf("default: control_enabled = %v", off["control_enabled"])
	}
}

func statsBody(t *testing.T, h *Handler) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stats: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

// Latest stats must carry the settings the miner now runs at, so the UI can
// show current frequency/voltage and a paused state.
func TestStatsViewIncludesFrequencyVoltagePaused(t *testing.T) {
	s, _, reg := newControlStore(t)
	if err := s.RecordStats("ctl-miner", base.Stats{
		HashRateGH:    1000,
		Timestamp:     time.Now(),
		Frequency:     618.5,
		CoreVoltageMv: 1150,
		Paused:        true,
	}); err != nil {
		t.Fatalf("RecordStats: %v", err)
	}

	h := New(s, WithConfig(controlConfig(true)), WithRegistry(reg))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/miners", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("miners: %d %s", rec.Code, rec.Body.String())
	}

	var got struct {
		Miners []struct {
			ID    string `json:"id"`
			Stats *struct {
				Frequency     float64 `json:"frequency"`
				CoreVoltageMv int     `json:"core_voltage_mv"`
				Paused        bool    `json:"paused"`
			} `json:"stats"`
		} `json:"miners"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, m := range got.Miners {
		if m.ID != "ctl-miner" {
			continue
		}
		if m.Stats == nil {
			t.Fatal("ctl-miner stats missing")
		}
		if m.Stats.Frequency != 618.5 || m.Stats.CoreVoltageMv != 1150 || !m.Stats.Paused {
			t.Fatalf("stats = %+v, want freq 618.5, 1150 mV, paused", m.Stats)
		}
	}
}
