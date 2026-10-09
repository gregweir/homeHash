package poller

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gregweir/homeHash/internal/drivers"
	"github.com/gregweir/homeHash/internal/drivers/base"
	"github.com/gregweir/homeHash/internal/store"
)

// fakeDriver returns canned stats, or errors for IPs it is told to fail on.
type fakeDriver struct {
	name    string
	stats   base.Stats
	failIPs map[string]bool
	polls   atomic.Int64
}

func (f *fakeDriver) Name() string                    { return f.name }
func (f *fakeDriver) SupportedModels() []string       { return []string{"Fake One"} }
func (f *fakeDriver) Discover() ([]base.Miner, error) { return nil, nil }
func (f *fakeDriver) Poll(m base.Miner) (base.Stats, error) {
	f.polls.Add(1)
	if f.failIPs[m.IP] {
		return base.Stats{}, errors.New("connection refused")
	}
	return f.stats, nil
}

func newTestFixtures(t *testing.T, d base.MinerDriver) (*store.Store, *drivers.Registry) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	reg := drivers.New() // bitaxe registered, but no real miners seeded
	if d != nil {
		reg.Register(d)
	}
	return s, reg
}

func TestTickRecordsStatsAndUpdatesLastSeen(t *testing.T) {
	s, reg := newTestFixtures(t, &fakeDriver{
		name:  "fake",
		stats: base.Stats{HashRateGH: 450.2, TemperatureC: 58, Timestamp: time.Now()},
	})

	miner := base.Miner{ID: "miner-1", IP: "10.0.0.1", Model: "Fake One", Driver: "fake"}
	if err := s.UpsertMiner(miner); err != nil {
		t.Fatalf("seed miner: %v", err)
	}

	p := New(s, reg, time.Minute)
	before := time.Now()
	if err := p.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	hist, err := s.History("miner-1", time.Time{})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("want 1 stat point, got %d", len(hist))
	}
	if hist[0].HashRateGH != 450.2 {
		t.Errorf("hash rate: want 450.2, got %v", hist[0].HashRateGH)
	}

	got, err := s.Miners()
	if err != nil {
		t.Fatalf("Miners: %v", err)
	}
	if got[0].LastSeen.Before(before) {
		t.Errorf("LastSeen not updated: %v < %v", got[0].LastSeen, before)
	}
}

func TestTickContinuesWhenPollFails(t *testing.T) {
	s, reg := newTestFixtures(t, &fakeDriver{
		name:    "fake",
		stats:   base.Stats{HashRateGH: 100, Timestamp: time.Now()},
		failIPs: map[string]bool{"10.0.0.2": true},
	})

	seed := []base.Miner{
		{ID: "good", IP: "10.0.0.1", Driver: "fake"},
		{ID: "broken", IP: "10.0.0.2", Driver: "fake"},
	}
	for _, m := range seed {
		if err := s.UpsertMiner(m); err != nil {
			t.Fatalf("seed miner: %v", err)
		}
	}

	p := New(s, reg, time.Minute)
	if err := p.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	hist, err := s.History("good", time.Time{})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 1 {
		t.Errorf("good miner: want 1 stat point, got %d", len(hist))
	}
}

func TestTickSkipsMinersWithUnknownDriver(t *testing.T) {
	s, reg := newTestFixtures(t, nil) // only bitaxe registered, no fake

	miner := base.Miner{ID: "ghost", IP: "10.0.0.3", Driver: "ghost"}
	if err := s.UpsertMiner(miner); err != nil {
		t.Fatalf("seed miner: %v", err)
	}

	p := New(s, reg, time.Minute)
	if err := p.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	hist, err := s.History("ghost", time.Time{})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 0 {
		t.Errorf("want no stats recorded, got %d points", len(hist))
	}
}

// fakeDriver implements base.InfoUpdater: it renames the miner's Model on every pass.
type identifyingDriver struct {
	fakeDriver
	model string
}

func (f *identifyingDriver) UpdateInfo(m base.Miner) (base.Miner, error) {
	m.Model = f.model
	return m, nil
}

func TestTickUpdatesMinerIdentityWhenDriverSupportsIt(t *testing.T) {
	d := &identifyingDriver{
		fakeDriver: fakeDriver{name: "fake", stats: base.Stats{HashRateGH: 99, Timestamp: time.Now()}},
		model:      "Gamma 601",
	}
	s, reg := newTestFixtures(t, d)

	miner := base.Miner{ID: "miner-1", IP: "10.0.0.1", Driver: "fake"}
	if err := s.UpsertMiner(miner); err != nil {
		t.Fatalf("seed miner: %v", err)
	}

	p := New(s, reg, time.Minute)
	if err := p.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := s.Miners()
	if err != nil {
		t.Fatalf("Miners: %v", err)
	}
	if got[0].Model != "Gamma 601" {
		t.Errorf("Model should be enriched by driver: got %q", got[0].Model)
	}
}

func TestRunPollsRepeatedlyUntilContextCancelled(t *testing.T) {
	d := &fakeDriver{
		name:  "fake",
		stats: base.Stats{HashRateGH: 50, Timestamp: time.Now()},
	}
	s, reg := newTestFixtures(t, d)
	miner := base.Miner{ID: "miner-1", IP: "10.0.0.1", Driver: "fake"}
	if err := s.UpsertMiner(miner); err != nil {
		t.Fatalf("seed miner: %v", err)
	}

	p := New(s, reg, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	// Wait until at least 2 polls have happened, then stop.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if d.polls.Load() >= 2 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	<-done

	if polls := d.polls.Load(); polls < 2 {
		t.Errorf("want at least 2 polls, got %d", polls)
	}
}
