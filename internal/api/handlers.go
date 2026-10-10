// Package api provides HTTP handlers for the homeHash REST API.
// All routes return JSON and are designed for both the web UI and programmatic access.
package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gregweir/homeHash/internal/config"
	"github.com/gregweir/homeHash/internal/drivers"
	"github.com/gregweir/homeHash/internal/drivers/base"
	"github.com/gregweir/homeHash/internal/store"
)

// Handler holds dependencies for HTTP handlers.
type Handler struct {
	store *store.Store
	mux   *http.ServeMux
	// cfg gates and clamps write operations; nil means control (and any
	// other config-gated feature) is off.
	cfg *config.Config
	// registry resolves a miner's driver for write operations; nil disables
	// control regardless of cfg.
	registry *drivers.Registry
	// version is stamped by the release build (WithVersion); "dev" or a
	// hardcoded fallback otherwise.
	version string
	// eventInterval is how often /api/events checks for new data;
	// zero means the default.
	eventInterval time.Duration
}

// Option customizes a Handler at construction.
type Option func(*Handler)

// WithVersion sets the build-injected version reported by /api/health.
func WithVersion(v string) Option {
	return func(h *Handler) { h.version = v }
}

// WithConfig enables config-gated features (control write endpoints).
func WithConfig(cfg *config.Config) Option {
	return func(h *Handler) { h.cfg = cfg }
}

// WithRegistry enables driver dispatch for control operations.
func WithRegistry(reg *drivers.Registry) Option {
	return func(h *Handler) { h.registry = reg }
}

// New creates a new Handler backed by the given store.
func New(s *store.Store, opts ...Option) *Handler {
	h := &Handler{store: s, mux: http.NewServeMux()}
	for _, opt := range opts {
		opt(h)
	}
	h.RegisterRoutes(h.mux)
	return h
}

// ServeHTTP routes through the handler's own mux.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// RegisterRoutes sets up all API routes on the given mux.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", h.Health)
	mux.HandleFunc("GET /api/miners", h.ListMiners)
	mux.HandleFunc("GET /api/miners/{id}", h.GetMiner)
	mux.HandleFunc("GET /api/miners/{id}/history", h.MinerHistory)
	mux.HandleFunc("GET /api/miners/{id}/export/{format}", h.ExportHistory)
	mux.HandleFunc("POST /api/miners/{id}/settings", h.ApplyMinerSettings)
	mux.HandleFunc("POST /api/miners/{id}/pause", h.PauseMiner)
	mux.HandleFunc("POST /api/miners/{id}/resume", h.ResumeMiner)
	mux.HandleFunc("GET /api/alerts", h.ListAlerts)
	mux.HandleFunc("GET /api/stats", h.Stats)
	mux.HandleFunc("GET /api/events", h.Events)
}

// Health returns service health status.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"version":   versionString(h),
	})
}

// statsView is the JSON shape of a stat point for the API and web UI.
type statsView struct {
	HashRateGH    float64 `json:"hash_rate_gh"`
	Accepted      int64   `json:"accepted"`
	Rejected      int64   `json:"rejected"`
	TemperatureC  float64 `json:"temperature_c"`
	FanSpeed      int     `json:"fan_speed"`
	PowerW        float64 `json:"power_w"`
	UptimeSeconds int64   `json:"uptime_seconds"`
	Timestamp     string  `json:"timestamp"`
	Frequency     float64 `json:"frequency"`
	CoreVoltageMv int     `json:"core_voltage_mv"`
	Paused        bool    `json:"paused"`
}

// minerView is the JSON shape of a miner for the API and web UI.
type minerView struct {
	ID       string     `json:"id"`
	IP       string     `json:"ip"`
	Model    string     `json:"model,omitempty"`
	Driver   string     `json:"driver"`
	PoolURL  string     `json:"pool_url,omitempty"`
	Firmware string     `json:"firmware,omitempty"`
	MAC      string     `json:"mac,omitempty"`
	Status   string     `json:"status"`
	LastSeen string     `json:"last_seen,omitempty"`
	Stats    *statsView `json:"stats,omitempty"`
}

// minerBody builds the JSON view of one miner, with its latest stats.
func minerBody(h *Handler, m base.Miner) minerView {
	view := minerView{
		ID:       m.ID,
		IP:       m.IP,
		Model:    m.Model,
		Driver:   m.Driver,
		PoolURL:  m.PoolURL,
		Firmware: m.Firmware,
		MAC:      m.MAC,
		Status:   statusFor(m),
	}
	if !m.LastSeen.IsZero() {
		view.LastSeen = m.LastSeen.UTC().Format(time.RFC3339)
	}
	latest, err := h.store.LatestStats(m.ID)
	if err == nil && latest != nil {
		sv := statsViewOf(*latest)
		view.Stats = &sv
	}
	return view
}

// maxHistoryHours caps the ?hours= param on history endpoints (7 days).
const maxHistoryHours = 168

// ListMiners returns all discovered miners with their latest stats.
func (h *Handler) ListMiners(w http.ResponseWriter, r *http.Request) {
	miners, err := h.store.Miners()
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
		return
	}

	out := make([]minerView, 0, len(miners))
	for _, m := range miners {
		out = append(out, minerBody(h, m))
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{"miners": out})
}

// latestStats fetches the most recent stat point for a miner, nil if none.
func (h *Handler) latestStats(minerID string) *statsView {
	latest, err := h.store.LatestStats(minerID)
	if err != nil || latest == nil {
		return nil
	}
	sv := statsViewOf(*latest)
	return &sv
}

// statusFor derives an online/offline label from recency of LastSeen.
func statusFor(m base.Miner) string {
	if m.LastSeen.After(time.Now().Add(-5 * time.Minute)) {
		return "online"
	}
	return "offline"
}

// GetMiner returns details for a single miner, including its latest stats.
func (h *Handler) GetMiner(w http.ResponseWriter, r *http.Request) {
	m, ok := h.lookupMiner(w, r)
	if !ok {
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"miner": minerBody(h, m),
	})
}

// MinerHistory returns a miner's stat history for a number of hours.
func (h *Handler) MinerHistory(w http.ResponseWriter, r *http.Request) {
	m, ok := h.lookupMiner(w, r)
	if !ok {
		return
	}

	hours := queryHours(r, 24)
	data, err := h.store.History(m.ID, time.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
		return
	}

	points := make([]statsView, 0, len(data))
	for _, st := range data {
		points = append(points, statsViewOf(st))
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"miner_id": m.ID,
		"hours":    hours,
		"data":     points,
	})
}

// alertView is the JSON shape of one stored alert. Times are RFC3339.
type alertView struct {
	Time    string `json:"time"`
	Kind    string `json:"kind"`
	Level   string `json:"level"`
	MinerID string `json:"miner_id"`
	MinerIP string `json:"miner_ip"`
	Model   string `json:"model"`
	Message string `json:"message"`
}

// ListAlerts returns recent alerts, newest first. ?limit caps the list
// (default 50).
func (h *Handler) ListAlerts(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	alerts, err := h.store.ListAlerts(limit)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
		return
	}

	views := make([]alertView, 0, len(alerts))
	for _, a := range alerts {
		views = append(views, alertView{
			Time:    a.Time.UTC().Format(time.RFC3339Nano),
			Kind:    string(a.Kind),
			Level:   string(a.Level),
			MinerID: a.MinerID,
			MinerIP: a.MinerIP,
			Model:   a.Model,
			Message: a.Message,
		})
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"alerts": views})
}

// ExportHistory downloads a miner's stat history as CSV or JSON, with a
// Content-Disposition attachment header so browsers save it directly.
func (h *Handler) ExportHistory(w http.ResponseWriter, r *http.Request) {
	m, ok := h.lookupMiner(w, r)
	if !ok {
		return
	}

	format := r.PathValue("format")
	if format != "csv" && format != "json" {
		respondJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("unsupported format %q (use csv or json)", format),
		})
		return
	}

	hours := queryHours(r, 24)
	data, err := h.store.History(m.ID, time.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("%s-history-%dh.%s", m.ID, hours, format)))

	views := make([]statsView, 0, len(data))
	for _, st := range data {
		views = append(views, statsViewOf(st))
	}

	if format == "json" {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"miner_id": m.ID,
			"hours":    hours,
			"data":     views,
		})
		return
	}

	writeHistoryCSV(w, views)
}

// historyCSVHeader is the CSV export column order.
var historyCSVHeader = []string{
	"timestamp", "hash_rate_gh", "power_w", "temperature_c",
	"fan_speed", "accepted", "rejected", "uptime_seconds",
}

func writeHistoryCSV(w http.ResponseWriter, points []statsView) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	if err := cw.Write(historyCSVHeader); err != nil {
		log.Printf("api: write csv header: %v", err)
		return
	}
	for _, p := range points {
		if err := cw.Write([]string{
			p.Timestamp,
			fmtG(p.HashRateGH),
			fmtG(p.PowerW),
			fmtG(p.TemperatureC),
			strconv.Itoa(p.FanSpeed),
			strconv.FormatInt(p.Accepted, 10),
			strconv.FormatInt(p.Rejected, 10),
			strconv.FormatInt(p.UptimeSeconds, 10),
		}); err != nil {
			log.Printf("api: write csv row: %v", err)
			return
		}
	}
	cw.Flush()
}

// fmtG prints floats roundly for CSV/JSON consumption.
func fmtG(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// lookupMiner resolves the {id} path value, writing a 404 when unknown.
func (h *Handler) lookupMiner(w http.ResponseWriter, r *http.Request) (base.Miner, bool) {
	id := r.PathValue("id")
	m, err := h.store.GetMiner(id)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
		return base.Miner{}, false
	}
	if m == nil {
		respondJSON(w, http.StatusNotFound, map[string]string{
			"error": "miner not found",
		})
		return base.Miner{}, false
	}
	return *m, true
}

// queryHours parses the hours param, clamped to [1, 168] with a default.
func queryHours(r *http.Request, def int) int {
	v := r.URL.Query().Get("hours")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < 1 {
		n = 1
	}
	if n > maxHistoryHours {
		n = maxHistoryHours
	}
	return n
}

// --- Miner control (settings, pause, resume) ---

// controlFor runs the shared write guards — control configured on, miner
// known, driver able to control the device — and returns the resolved
// driver. When ok is false the response has already been written.
func (h *Handler) controlFor(w http.ResponseWriter, r *http.Request) (base.ControlUpdater, base.Miner, bool) {
	if !h.controlEnabled() {
		respondJSON(w, http.StatusForbidden, map[string]string{
			"error": "control is disabled (set HOMEHASH_CONTROL=1)",
		})
		return nil, base.Miner{}, false
	}

	m, ok := h.lookupMiner(w, r)
	if !ok {
		return nil, base.Miner{}, false
	}

	drv, found := h.registry.Get(m.Driver)
	cu, supported := drv.(base.ControlUpdater)
	if !found || !supported {
		respondJSON(w, http.StatusNotImplemented, map[string]string{
			"error": fmt.Sprintf("driver %q does not support control", m.Driver),
		})
		return nil, base.Miner{}, false
	}
	return cu, m, true
}

// settingsReq is the request body of POST /api/miners/{id}/settings. Only
// provided keys are changed on the device.
type settingsReq struct {
	Frequency   *float64 `json:"frequency"`    // ASIC frequency, MHz
	CoreVoltage *float64 `json:"core_voltage"` // core voltage, mV
}

// ApplyMinerSettings patches a miner's frequency and/or core voltage.
func (h *Handler) ApplyMinerSettings(w http.ResponseWriter, r *http.Request) {
	cu, m, ok := h.controlFor(w, r)
	if !ok {
		return
	}

	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "read body"})
		return
	}
	var req settingsReq
	if err := json.Unmarshal(b, &req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid JSON body",
		})
		return
	}

	patch := base.SettingsPatch{}
	if req.Frequency != nil {
		f := *req.Frequency
		if f < h.cfg.ControlFreqMin || f > h.cfg.ControlFreqMax {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("frequency %.1f MHz out of range (%.0f–%.0f MHz)",
					f, h.cfg.ControlFreqMin, h.cfg.ControlFreqMax),
			})
			return
		}
		patch.Frequency = &f
	}
	if req.CoreVoltage != nil {
		v := *req.CoreVoltage
		if v != math.Floor(v) {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("core voltage %v must be an integer number of mV", v),
			})
			return
		}
		if v < float64(h.cfg.ControlVoltMin) || v > float64(h.cfg.ControlVoltMax) {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("core voltage %.0f mV out of range (%d–%d mV)",
					v, h.cfg.ControlVoltMin, h.cfg.ControlVoltMax),
			})
			return
		}
		iv := int(v)
		patch.CoreVoltage = &iv
	}
	if patch.Frequency == nil && patch.CoreVoltage == nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{
			"error": "no settings provided (frequency and/or core_voltage)",
		})
		return
	}

	if err := cu.ApplySettings(m, patch); err != nil {
		respondJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{
		"status": "applied",
		"miner":  m.ID,
	})
}

// PauseMiner pauses mining on the miner; ResumeMiner resumes it.
func (h *Handler) PauseMiner(w http.ResponseWriter, r *http.Request) {
	h.mineToggle(w, r, "pause")
}

func (h *Handler) ResumeMiner(w http.ResponseWriter, r *http.Request) {
	h.mineToggle(w, r, "resume")
}

func (h *Handler) mineToggle(w http.ResponseWriter, r *http.Request, op string) {
	cu, m, ok := h.controlFor(w, r)
	if !ok {
		return
	}
	var err error
	switch op {
	case "pause":
		err = cu.Pause(m)
	default:
		err = cu.Resume(m)
	}
	if err != nil {
		respondJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{
		"status": op,
		"miner":  m.ID,
	})
}

func statsViewOf(st base.Stats) statsView {
	return statsView{
		HashRateGH:    st.HashRateGH,
		Accepted:      st.Accepted,
		Rejected:      st.Rejected,
		TemperatureC:  st.TemperatureC,
		FanSpeed:      st.FanSpeed,
		PowerW:        st.PowerW,
		UptimeSeconds: int64(st.Uptime / time.Second),
		Timestamp:     st.Timestamp.UTC().Format(time.RFC3339),
		Frequency:     st.Frequency,
		CoreVoltageMv: st.CoreVoltageMv,
		Paused:        st.Paused,
	}
}

// Stats returns aggregate farm statistics (flat, matching docs/API.md).
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.aggregate())
}

// farmSnapshot builds a full farm view: aggregates plus per-miner bodies.
func (h *Handler) farmSnapshot() map[string]interface{} {
	farm := h.aggregate()
	miners, _ := h.store.Miners()
	mb := make([]minerView, 0, len(miners))
	for _, m := range miners {
		mb = append(mb, minerBody(h, m))
	}
	return map[string]interface{}{
		"farm":   farm,
		"miners": mb,
	}
}

// aggregate computes the farm summary numbers from the store.
func (h *Handler) aggregate() map[string]interface{} {
	miners, err := h.store.Miners()
	if err != nil {
		log.Printf("aggregate: %v", err)
		miners = nil
	}

	var (
		online     int
		offline    int
		hashrateGH float64
		powerW     float64
		accepted   int64
		rejected   int64
	)
	var tempSum float64
	var tempCount int
	for _, m := range miners {
		if statusFor(m) == "online" {
			online++
		} else {
			offline++
			continue
		}
		st, err := h.store.LatestStats(m.ID)
		if err != nil || st == nil {
			continue
		}
		hashrateGH += st.HashRateGH
		powerW += st.PowerW
		accepted += st.Accepted
		rejected += st.Rejected
		if st.TemperatureC > 0 {
			tempSum += st.TemperatureC
			tempCount++
		}
	}

	return map[string]interface{}{
		"total_miners":        len(miners),
		"online":              online,
		"offline":             offline,
		"total_hashrate_gh":   hashrateGH,
		"total_power_w":       powerW,
		"avg_temperature_c":   tempAvg(tempSum, tempCount),
		"efficiency_gh_per_w": efficiency(hashrateGH, powerW),
		"total_accepted":      accepted,
		"total_rejected":      rejected,
		"control_enabled":     h.controlEnabled(),
		"timestamp":           time.Now().UTC().Format(time.RFC3339),
	}
}

// controlEnabled reports whether write operations are configured on.
func (h *Handler) controlEnabled() bool {
	return h.cfg != nil && h.cfg.ControlEnabled && h.registry != nil
}

// versionString is the build-reported version, "dev" for unstamped builds.
func versionString(h *Handler) string {
	if h.version != "" {
		return h.version
	}
	return "dev"
}

// efficiency is GH per watt (higher is better); 0 if power is unknown.
func efficiency(hashrateGH, powerW float64) float64 {
	if powerW <= 0 {
		return 0
	}
	return hashrateGH / powerW
}

// tempAvg avoids dividing by zero when no online miner reports a temperature.
func tempAvg(sum float64, n int) float64 {
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// Events streams live farm snapshots over Server-Sent Events.
// A snapshot is emitted whenever a miner's data changes since the last send.
func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")

	interval := h.eventInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastPayload string
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}

		snapshot, err := json.Marshal(h.farmSnapshot())
		if err != nil {
			log.Printf("marshal snapshot: %v", err)
			continue
		}
		if string(snapshot) == lastPayload {
			continue
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", snapshot); err != nil {
			return // client went away
		}
		flusher.Flush()
		lastPayload = string(snapshot)
	}
}

// respondJSON writes a JSON response with the given status code.
func respondJSON(w http.ResponseWriter, code int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(payload)
}
