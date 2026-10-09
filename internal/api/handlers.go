// Package api provides HTTP handlers for the homeHash REST API.
// All routes return JSON and are designed for both the web UI and programmatic access.
package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gregweir/homeHash/internal/drivers/base"
	"github.com/gregweir/homeHash/internal/store"
)

// Handler holds dependencies for HTTP handlers.
type Handler struct {
	store *store.Store
	mux   *http.ServeMux
	// eventInterval is how often /api/events checks for new data;
	// zero means the default.
	eventInterval time.Duration
}

// New creates a new Handler backed by the given store.
func New(s *store.Store) *Handler {
	h := &Handler{store: s, mux: http.NewServeMux()}
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
	mux.HandleFunc("GET /api/stats", h.Stats)
	mux.HandleFunc("GET /api/events", h.Events)
}

// Health returns service health status.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"version":   "0.1.0",
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
		"timestamp":           time.Now().UTC().Format(time.RFC3339),
	}
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
