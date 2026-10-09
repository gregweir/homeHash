// Package alerts watches miner state and emits offline / overheating alerts
// through configured notifiers. It reads the store rather than hooking the
// poller, so alerting keeps working even across restarts and poll pauses.
package alerts

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gregweir/homeHash/internal/drivers/base"
)

// Kind names the condition an alert reports.
type Kind string

const (
	KindOffline       Kind = "offline"
	KindTemperature   Kind = "temperature"
	KindRecovered     Kind = "recovered"
	KindTempRecovered Kind = "temp-recovered"
)

// Level is an alert's severity.
type Level string

const (
	LevelInfo     Level = "info"
	LevelWarning  Level = "warning"
	LevelCritical Level = "critical"
)

// Alert is one notification about one miner.
type Alert struct {
	Kind    Kind
	Level   Level
	MinerID string
	MinerIP string
	Model   string
	Message string
}

// Config holds the evaluator thresholds. Zero values fall back to defaults.
type Config struct {
	TempWarn     float64
	TempCritical float64
	OfflineAfter time.Duration
	Cooldown     time.Duration
	// Now is only set in tests; nil means time.Now.
	Now func() time.Time
}

// tempLevel tracks a miner's temperature-alert state.
type tempLevel int

const (
	tempNone tempLevel = iota
	tempWarn
	tempCritical
)

// minerState persists one miner's alert state across evaluations.
type minerState struct {
	offline        bool
	temp           tempLevel
	lastTempNotify time.Time
}

// Store is the read surface the alert loop needs.
type Store interface {
	Miners() ([]base.Miner, error)
	LatestStats(minerID string) (*base.Stats, error)
}

// Evaluator turns miner state into alerts, remembering what it already
// alerted about so each condition notifies once, not every cycle.
type Evaluator struct {
	cfg    Config
	levels map[string]*tempLevelState
}

// tempLevelState is the per-miner temperature state.
type tempLevelState struct {
	level          tempLevel
	lastTempNotify time.Time
	offline        bool
}

// NewEvaluator creates an evaluator with the given configuration.
func NewEvaluator(cfg Config) *Evaluator {
	return &Evaluator{cfg: cfg, levels: make(map[string]*tempLevelState)}
}

func (e *Evaluator) now() time.Time {
	if e.cfg.Now != nil {
		return e.cfg.Now()
	}
	return time.Now()
}

// Evaluate returns the alerts due for one miner this cycle.
func (e *Evaluator) Evaluate(miner base.Miner, st *base.Stats) []Alert {
	cfg := e.cfg
	// Never-polled miners have no LastSeen; skip them until we have data.
	if miner.LastSeen.IsZero() {
		return nil
	}
	now := e.now()

	state := e.levels[miner.ID]
	if state == nil {
		state = &tempLevelState{}
		e.levels[miner.ID] = state
	}

	// Offline: LastSeen is stale because a successful poll refreshes it.
	if now.Sub(miner.LastSeen) > e.offlineAfter() {
		if !state.offline {
			state.offline = true
			// The offline alert already covers the miner; clear any
			// temperature alert silently.
			state.level = tempNone
			state.lastTempNotify = time.Time{}
			return []Alert{{
				Kind: KindOffline, Level: LevelCritical,
				MinerID: miner.ID, MinerIP: miner.IP, Model: miner.Model,
				Message: fmt.Sprintf("miner offline: last poll %s ago",
					(now.Sub(miner.LastSeen)).Round(time.Second)),
			}}
		}
		return nil
	}

	var alerts []Alert
	if state.offline {
		state.offline = false
		alerts = append(alerts, Alert{
			Kind: KindRecovered, Level: LevelInfo,
			MinerID: miner.ID, MinerIP: miner.IP, Model: miner.Model,
			Message: "back online",
		})
	}

	if st == nil {
		return alerts
	}

	level := tempNone
	switch {
	case st.TemperatureC >= cfg.TempCritical:
		level = tempCritical
	case st.TemperatureC >= cfg.TempWarn:
		level = tempWarn
	}

	if level > tempNone {
		same := level == state.level
		if !same || now.Sub(state.lastTempNotify) >= e.cooldown() {
			lvl := LevelWarning
			threshold := cfg.TempWarn
			if level == tempCritical {
				lvl = LevelCritical
				threshold = cfg.TempCritical
			}
			state.level = level
			state.lastTempNotify = now
			alerts = append(alerts, Alert{
				Kind: KindTemperature, Level: lvl,
				MinerID: miner.ID, MinerIP: miner.IP, Model: miner.Model,
				Message: fmt.Sprintf("temperature %.1f°C above %.0f°C threshold",
					st.TemperatureC, threshold),
			})
		}
		return alerts
	}

	// Back within range → recovery, once.
	if state.level > tempNone {
		state.level = tempNone
		state.lastTempNotify = time.Time{}
		alerts = append(alerts, Alert{
			Kind: KindTempRecovered, Level: LevelInfo,
			MinerID: miner.ID, MinerIP: miner.IP, Model: miner.Model,
			Message: fmt.Sprintf("temperature back to %.1f°C", st.TemperatureC),
		})
	}
	return alerts
}

func (e *Evaluator) offlineAfter() time.Duration {
	if e.cfg.OfflineAfter > 0 {
		return e.cfg.OfflineAfter
	}
	return 10 * time.Minute
}

func (e *Evaluator) cooldown() time.Duration {
	if e.cfg.Cooldown > 0 {
		return e.cfg.Cooldown
	}
	return 15 * time.Minute
}

// Notifier receives each alert.
type Notifier interface {
	Notify(ctx context.Context, a Alert) error
}

// Run evaluates every miner once per interval and notifies about new alerts.
// It returns when ctx is cancelled.
func Run(ctx context.Context, st Store, e *Evaluator, n Notifier, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	checkOnce := func() {
		miners, err := st.Miners()
		if err != nil {
			log.Printf("alerts: list miners: %v", err)
			return
		}
		for _, m := range miners {
			stats, err := st.LatestStats(m.ID)
			if err != nil {
				log.Printf("alerts: latest stats for %s: %v", m.ID, err)
				continue
			}
			for _, a := range e.Evaluate(m, stats) {
				if err := n.Notify(ctx, a); err != nil {
					log.Printf("alerts: notify (kind=%s level=%s miner=%s): %v",
						a.Kind, a.Level, a.MinerID, err)
				}
			}
		}
	}

	checkOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkOnce()
		}
	}
}

// FormatAlert renders an alert as a single human-readable line.
func FormatAlert(a Alert) string {
	subj := "homeHash"
	if a.Model != "" {
		subj = fmt.Sprintf("%s (%s)", a.Model, a.MinerIP)
	} else if a.MinerIP != "" {
		subj = a.MinerIP
	}
	return fmt.Sprintf("%s [%s] %s: %s", subj, a.Level, a.Kind, a.Message)
}
