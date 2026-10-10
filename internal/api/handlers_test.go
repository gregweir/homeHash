package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gregweir/homeHash/internal/drivers/base"
	"github.com/gregweir/homeHash/internal/store"
)

func TestEventsStreamsSnapshotOnDataChange(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	now := time.Now()
	if err := s.UpsertMiner(base.Miner{ID: "miner-1", IP: "10.0.0.1", Driver: "bitaxe", LastSeen: now}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.RecordStats("miner-1", base.Stats{
		HashRateGH: 1234.5, TemperatureC: 55, PowerW: 22, Timestamp: now,
	}); err != nil {
		t.Fatalf("RecordStats: %v", err)
	}

	h := New(s)
	h.eventInterval = 10 * time.Millisecond // short interval for the test

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	// The handler streams into rec while the test polls rec.Body; wrap it in
	// a lock so the race detector doesn't flag the (test-only) concurrent use.
	lrec := &lockedRecorder{rec: rec}

	done := make(chan struct{})
	go func() {
		h.ServeHTTP(lrec, req)
		close(done)
	}()

	// Wait until at least one event (or two — proving data change) is streamed.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		bodyNow := lrec.body()
		if strings.Count(bodyNow, "data: ") >= 1 &&
			strings.Contains(bodyNow, `"total_hashrate_gh":1234.5`) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not return after context cancel")
	}

	body := lrec.body()
	if err := checkContentType(lrec.Header().Get("Content-Type"), "text/event-stream"); err != nil {
		t.Errorf("content type: %v", err)
	}
	if !strings.Contains(body, `"miners"`) {
		t.Errorf("snapshot missing miners: %s", body[:min(300, len(body))])
	}
	if !strings.Contains(body, `"total_hashrate_gh":1234.5`) {
		t.Errorf("snapshot missing farm stats: %s", body[:min(400, len(body))])
	}
	// Each event must end with a blank line (SSE framing).
	if !strings.Contains(body, "\n\n") {
		t.Error("events not framed with blank lines")
	}
}

func checkContentType(got, wantBase string) error {
	if got == "" || !strings.Contains(got, wantBase) {
		return errors.New("got " + got + ", want base " + wantBase)
	}
	return nil
}

// lockedRecorder serializes reads of the recording from the test with the
// handler-goroutine writes into it.
type lockedRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func (l *lockedRecorder) Header() http.Header {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Header()
}

func (l *lockedRecorder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Write(p)
}

func (l *lockedRecorder) WriteHeader(code int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rec.WriteHeader(code)
}

// Flush forwards to the recorder; ServeHTTP requires a Flusher.
func (l *lockedRecorder) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f, ok := any(l.rec).(http.Flusher); ok {
		f.Flush()
	}
}

func (l *lockedRecorder) body() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Body.String()
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestStatsAggregate(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	now := time.Now()
	seed := []struct {
		miner base.Miner
		stats base.Stats
	}{
		{base.Miner{ID: "a", IP: "10.0.0.1", Driver: "bitaxe", LastSeen: now},
			base.Stats{HashRateGH: 1200, TemperatureC: 54, PowerW: 20, Accepted: 1000, Rejected: 5, Timestamp: now}},
		{base.Miner{ID: "b", IP: "10.0.0.2", Driver: "bitaxe", LastSeen: now},
			base.Stats{HashRateGH: 2600, TemperatureC: 58, PowerW: 54, Accepted: 900, Rejected: 15, Timestamp: now}},
		// Offline miner: seeded but never polled.
		{base.Miner{ID: "c", IP: "10.0.0.3", Driver: "bitaxe"}, base.Stats{}},
	}
	for _, x := range seed {
		if err := s.UpsertMiner(x.miner); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if x.stats.Timestamp != (time.Time{}) {
			if err := s.RecordStats(x.miner.ID, x.stats); err != nil {
				t.Fatalf("RecordStats: %v", err)
			}
		}
	}

	h := New(s)
	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	rec := httptest.NewRecorder()
	h.Stats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var got struct {
		TotalMiners      int     `json:"total_miners"`
		Online           int     `json:"online"`
		Offline          int     `json:"offline"`
		TotalHashrate    float64 `json:"total_hashrate_gh"`
		TotalPower       float64 `json:"total_power_w"`
		EfficiencyGHPerW float64 `json:"efficiency_gh_per_w"`
		Timestamp        string  `json:"timestamp"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.TotalMiners != 3 || got.Online != 2 || got.Offline != 1 {
		t.Errorf("counts: got %d/%d/%d, want 3/2/1", got.TotalMiners, got.Online, got.Offline)
	}
	if got.TotalHashrate < 3799.9 || got.TotalHashrate > 3800.1 {
		t.Errorf("total hash rate: got %v, want 3800", got.TotalHashrate)
	}
	if got.TotalPower < 73.9 || got.TotalPower > 74.1 {
		t.Errorf("total power: got %v, want 74", got.TotalPower)
	}
	// 3800 GH/s on 74 W = 51.35 GH/W
	if got.EfficiencyGHPerW < 51.2 || got.EfficiencyGHPerW > 51.5 {
		t.Errorf("efficiency: got %v, want ~51.3", got.EfficiencyGHPerW)
	}
	if got.Timestamp == "" {
		t.Error("timestamp missing")
	}
}

func TestGetMinerReturnsDetailWithHistory(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	miner := base.Miner{ID: "miner-1", IP: "10.0.0.1", Model: "Gamma 601", Driver: "bitaxe", LastSeen: time.Now()}
	if err := s.UpsertMiner(miner); err != nil {
		t.Fatalf("seed: %v", err)
	}
	now := time.Now()
	for i := 1; i <= 3; i++ {
		st := base.Stats{HashRateGH: float64(400 + i), TemperatureC: 58, Timestamp: now.Add(time.Duration(i) * time.Minute)}
		if err := s.RecordStats("miner-1", st); err != nil {
			t.Fatalf("RecordStats: %v", err)
		}
	}

	// Detail endpoint includes the miner and their latest stats.
	h := New(s)
	req := httptest.NewRequest(http.MethodGet, "/api/miners/miner-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var detail struct {
		Miner struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Stats *struct {
				HashRateGH float64 `json:"hash_rate_gh"`
			} `json:"stats"`
		} `json:"miner"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Miner.ID != "miner-1" || detail.Miner.Model != "Gamma 601" {
		t.Errorf("detail: got %+v", detail)
	}
	if detail.Miner.Stats == nil || detail.Miner.Stats.HashRateGH <= 400 {
		t.Errorf("detail stats missing/incorrect: %+v", detail.Miner.Stats)
	}

	// History endpoint returns points, oldest first, honor hours param.
	req = httptest.NewRequest(http.MethodGet, "/api/miners/miner-1/history?hours=1", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("history status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var hist struct {
		MinerID string `json:"miner_id"`
		Data    []struct {
			HashRateGH float64 `json:"hash_rate_gh"`
			Timestamp  string  `json:"timestamp"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hist); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if hist.MinerID != "miner-1" || len(hist.Data) != 3 {
		t.Fatalf("history: got %+v, want 3 points for miner-1", hist)
	}
	if hist.Data[0].HashRateGH > hist.Data[2].HashRateGH {
		t.Error("history not oldest-first")
	}
}

func TestGetMinerNotFound(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	h := New(s)
	req := httptest.NewRequest(http.MethodGet, "/api/miners/nope", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListMinersReturnsStoredMinersWithStats(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	seed := base.Miner{
		ID:     "miner-1",
		IP:     "10.0.0.1",
		Model:  "Bitaxe Gamma",
		Driver: "bitaxe",
	}
	if err := s.UpsertMiner(seed); err != nil {
		t.Fatalf("seed miner: %v", err)
	}
	st := base.Stats{
		HashRateGH:   450.2,
		Accepted:     100,
		Rejected:     2,
		TemperatureC: 58.4,
		FanSpeed:     72,
		PowerW:       20.86,
		Uptime:       90 * time.Second,
		Timestamp:    time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC),
	}
	if err := s.RecordStats("miner-1", st); err != nil {
		t.Fatalf("RecordStats: %v", err)
	}

	h := New(s)
	req := httptest.NewRequest(http.MethodGet, "/api/miners", nil)
	rec := httptest.NewRecorder()
	h.ListMiners(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Miners []struct {
			ID    string `json:"id"`
			IP    string `json:"ip"`
			Model string `json:"model"`
			Stats *struct {
				HashRateGH    float64 `json:"hash_rate_gh"`
				PowerW        float64 `json:"power_w"`
				UptimeSeconds int64   `json:"uptime_seconds"`
				TemperatureC  float64 `json:"temperature_c"`
			} `json:"stats"`
		} `json:"miners"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Miners) != 1 {
		t.Fatalf("want 1 miner, got %d: %s", len(resp.Miners), rec.Body.String())
	}
	m := resp.Miners[0]
	if m.ID != "miner-1" || m.IP != "10.0.0.1" || m.Model != "Bitaxe Gamma" {
		t.Errorf("miner fields: got %+v", m)
	}
	if m.Stats == nil || m.Stats.HashRateGH != 450.2 {
		t.Errorf("stats: got %+v", m.Stats)
	}
	if m.Stats != nil {
		if m.Stats.PowerW != 20.86 {
			t.Errorf("PowerW: want 20.86, got %v", m.Stats.PowerW)
		}
		if m.Stats.UptimeSeconds != 90 {
			t.Errorf("UptimeSeconds: want 90, got %d", m.Stats.UptimeSeconds)
		}
	}
}

func TestListAlerts(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	now := time.Now()
	newest := base.Alert{
		Time: now, Kind: base.KindOffline, Level: base.LevelCritical,
		MinerID: "bitaxe-192-168-1-11", MinerIP: "192.168.1.11",
		Model: "Bitaxe GammaTurbo", Message: "miner offline: last poll 10m0s ago",
	}
	older := base.Alert{
		Time: now.Add(-time.Minute), Kind: base.KindTemperature, Level: base.LevelWarning,
		MinerID: "bitaxe-192-168-1-10", MinerIP: "192.168.1.10",
		Model: "Bitaxe Gamma 601", Message: "temperature 80.5°C above 75°C threshold",
	}
	for _, a := range []base.Alert{newest, older} {
		if err := s.AddAlert(a); err != nil {
			t.Fatalf("AddAlert: %v", err)
		}
	}

	h := New(s)

	// Plain fetch: newest first, alert view fields present.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/alerts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/alerts: status %d", rec.Code)
	}
	var got struct {
		Alerts []struct {
			Time    time.Time `json:"time"`
			Kind    string    `json:"kind"`
			Level   string    `json:"level"`
			MinerID string    `json:"miner_id"`
			MinerIP string    `json:"miner_ip"`
			Model   string    `json:"model"`
			Message string    `json:"message"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Alerts) != 2 {
		t.Fatalf("want 2 alerts, got %d", len(got.Alerts))
	}
	first, second := got.Alerts[0], got.Alerts[1]
	if first.Kind != "offline" || first.MinerID != newest.MinerID ||
		first.MinerIP != newest.MinerIP || first.Model != newest.Model ||
		first.Message != newest.Message {
		t.Fatalf("first alert = %+v, want newest %v", first, newest)
	}
	if first.Level != "critical" || second.Level != "warning" {
		t.Fatalf("levels = %q, %q", first.Level, second.Level)
	}
	if first.Time.Before(second.Time) {
		t.Fatalf("alerts not newest first: %v before %v", first.Time, second.Time)
	}

	// ?limit= caps the list.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/alerts?limit=1", nil))
	var limited struct {
		Alerts []json.RawMessage `json:"alerts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &limited); err != nil {
		t.Fatalf("decode limited: %v", err)
	}
	if len(limited.Alerts) != 1 {
		t.Fatalf("limit=1: want 1 alert, got %d", len(limited.Alerts))
	}
}

func TestListAlertsEmpty(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	rec := httptest.NewRecorder()
	New(s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/alerts", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"alerts":[]`) {
		t.Fatalf("want empty alerts array, got %s", rec.Body.String())
	}
}

func TestExportHistoryJSON(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	ts := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)
	m := base.Miner{ID: "bitaxe-192-168-1-10", IP: "192.168.1.10", Driver: "bitaxe", Model: "Bitaxe Gamma 601"}
	if err := s.UpsertMiner(m); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := s.RecordStats(m.ID, base.Stats{
			HashRateGH:   1200 + float64(i),
			Accepted:     int64(100 + i),
			Rejected:     int64(i),
			TemperatureC: 55 + float64(i),
			FanSpeed:     60 + i,
			PowerW:       20 + float64(i),
			Uptime:       time.Duration(i) * time.Minute,
			Timestamp:    ts.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("RecordStats: %v", err)
		}
	}

	h := New(s)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/miners/"+m.ID+"/export/json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export json: status %d body %s", rec.Code, rec.Body.String())
	}
	checkContentType(rec.Header().Get("Content-Type"), "application/json")
	if rec.Header().Get("Content-Disposition") == "" {
		t.Error("json export missing Content-Disposition")
	}

	var got struct {
		MinerID string `json:"miner_id"`
		Data    []struct {
			Timestamp     string  `json:"timestamp"`
			HashRateGH    float64 `json:"hash_rate_gh"`
			PowerW        float64 `json:"power_w"`
			TemperatureC  float64 `json:"temperature_c"`
			FanSpeed      int     `json:"fan_speed"`
			Accepted      int     `json:"accepted"`
			Rejected      int     `json:"rejected"`
			UptimeSeconds int64   `json:"uptime_seconds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.MinerID != m.ID || len(got.Data) != 3 {
		t.Fatalf("body = miner %s with %d points", got.MinerID, len(got.Data))
	}
	if got.Data[0].HashRateGH != 1200 || got.Data[2].HashRateGH != 1202 {
		t.Fatalf("points = %+v", got.Data)
	}
	if got.Data[1].UptimeSeconds != 60 {
		t.Fatalf("uptime = %d, want 60", got.Data[1].UptimeSeconds)
	}

	// CSV: same data, header row + points, oldest first.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/miners/"+m.ID+"/export/csv?hours=24", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export csv: status %d body %s", rec.Code, rec.Body.String())
	}
	checkContentType(rec.Header().Get("Content-Type"), "text/csv")
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("csv lines = %d, want header + 3 points: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "timestamp") || !strings.Contains(lines[0], "hash_rate_gh") ||
		!strings.Contains(lines[0], "power_w") || !strings.Contains(lines[0], "temperature_c") ||
		!strings.Contains(lines[0], "fan_speed") || !strings.Contains(lines[0], "uptime_seconds") {
		t.Fatalf("csv header wrong: %q", lines[0])
	}
	if strings.Contains(lines[1], "1200,") && !strings.HasPrefix(lines[1], "2026-10-09T20:00:00Z") {
		t.Fatalf("csv first row = %q, want oldest point first", lines[1])
	}

	// Bad format: 400, not a panic or 404.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/miners/"+m.ID+"/export/xml", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad format: want 400, got %d", rec.Code)
	}

	// Unknown miner: 404.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/miners/no-such/export/csv", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown miner: want 404, got %d", rec.Code)
	}
}

// The health endpoint reports the build-injected version, not a hardcoded
// number (the release workflow stamps it with -ldflags -X main.version).
func TestHealthReportsBuildVersion(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	h := New(s, WithVersion("v0.3.0"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var got struct {
		Version string `json:"version"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != "v0.3.0" || got.Status != "ok" {
		t.Fatalf("health = %+v, want version v0.3.0 status ok", got)
	}
}
