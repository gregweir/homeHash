// Package poller drives periodic stat collection from all known miners.
package poller

import (
	"context"
	"log"
	"time"

	"github.com/gregweir/homeHash/internal/drivers"
	"github.com/gregweir/homeHash/internal/drivers/base"
	"github.com/gregweir/homeHash/internal/store"
)

// Poller polls every known miner on a fixed interval and appends results to the store.
type Poller struct {
	store    *store.Store
	registry *drivers.Registry
	interval time.Duration
}

// New creates a Poller that ticks every interval.
func New(s *store.Store, reg *drivers.Registry, interval time.Duration) *Poller {
	return &Poller{store: s, registry: reg, interval: interval}
}

// Tick runs one polling pass over all known miners.
// Per-miner failures are logged and don't abort the pass; the returned error
// is reserved for store failures, which break the pass as a whole.
func (p *Poller) Tick(ctx context.Context) error {
	miners, err := p.store.Miners()
	if err != nil {
		return err
	}

	for _, m := range miners {
		if ctx.Err() != nil {
			return nil
		}

		driver, ok := p.registry.Get(m.Driver)
		if !ok {
			log.Printf("no driver %q for miner %s, skipping", m.Driver, m.ID)
			continue
		}

		stats, err := driver.Poll(m)
		if err != nil {
			log.Printf("poll %s (%s): %v", m.ID, m.IP, err)
			continue
		}

		// Drivers that implement base.InfoUpdater refresh identity fields
		// (model, firmware, pool). Failure to enrich is not fatal.
		if upd, ok := driver.(base.InfoUpdater); ok {
			if enriched, err := upd.UpdateInfo(m); err != nil {
				log.Printf("update info %s (%s): %v", m.ID, m.IP, err)
			} else {
				m = enriched
			}
		}

		if err := p.store.RecordStats(m.ID, stats); err != nil {
			return err
		}

		m.LastSeen = time.Now()
		if err := p.store.UpsertMiner(m); err != nil {
			return err
		}
	}
	return nil
}

// Run ticks on the configured interval until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	// Poll immediately so data exists before the first tick.
	if err := p.Tick(ctx); err != nil {
		log.Printf("poll pass: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.Tick(ctx); err != nil {
				log.Printf("poll pass: %v", err)
			}
		}
	}
}
