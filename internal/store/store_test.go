package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gregweir/homeHash/internal/drivers/base"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Miners() must come back in a deterministic order — the dashboard legend and
// chart draw order derive from it.
func TestMinersDeterministicOrder(t *testing.T) {
	s := openTestStore(t)

	for _, id := range []string{"zeta-9", "alpha-1", "mid-5"} {
		if err := s.UpsertMiner(base.Miner{ID: id, IP: "10.0.0.1", Driver: "bitaxe"}); err != nil {
			t.Fatalf("UpsertMiner %s: %v", id, err)
		}
	}
	ms, err := s.Miners()
	if err != nil {
		t.Fatalf("Miners: %v", err)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.ID)
	}
	want := []string{"alpha-1", "mid-5", "zeta-9"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Miners order = %v, want %v", got, want)
	}
}

// The server reads history and SSE snapshots constantly while the poller
// writes; none of those may surface SQLITE_BUSY to the caller.
func TestConcurrentReadersAndWritersNeverSeeBusy(t *testing.T) {
	s := openTestStore(t)

	m := base.Miner{ID: "m1", IP: "192.168.1.45", Driver: "bitaxe"}
	if err := s.UpsertMiner(m); err != nil {
		t.Fatalf("UpsertMiner: %v", err)
	}

	const workers, iters = 8, 40
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		go func() {
			var last error
			for i := 0; i < iters; i++ {
				if err := s.RecordStats(m.ID, base.Stats{HashRateGH: 100, Timestamp: time.Now()}); err != nil {
					last = err
				}
				if _, err := s.LatestStats(m.ID); err != nil {
					last = err
				}
				if _, err := s.Miners(); err != nil {
					last = err
				}
			}
			errs <- last
		}()
	}
	for w := 0; w < workers; w++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent access: %v", err)
		}
	}
}

func TestUpsertAndListMiners(t *testing.T) {
	s := openTestStore(t)

	m := base.Miner{
		ID:       "miner-abc",
		IP:       "192.168.1.45",
		Model:    "Bitaxe Gamma",
		Driver:   "bitaxe",
		PoolURL:  "stratum+tcp://pool.ocean.xyz:3334",
		Firmware: "2.1.4",
		LastSeen: time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC),
	}
	if err := s.UpsertMiner(m); err != nil {
		t.Fatalf("UpsertMiner: %v", err)
	}

	// Same ID: update fields instead of duplicating the row.
	updated := m
	updated.IP = "192.168.1.99"
	updated.PoolURL = "stratum+tcp://solo.ckpool.org:3333"
	if err := s.UpsertMiner(updated); err != nil {
		t.Fatalf("UpsertMiner (update): %v", err)
	}

	got, err := s.Miners()
	if err != nil {
		t.Fatalf("Miners: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 miner, got %d: %+v", len(got), got)
	}
	if got[0] != updated {
		t.Errorf("roundtrip mismatch:\n got %+v\nwant %+v", got[0], updated)
	}
}

func TestMinersEmpty(t *testing.T) {
	s := openTestStore(t)
	got, err := s.Miners()
	if err != nil {
		t.Fatalf("Miners: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want 0 miners, got %d", len(got))
	}
}

func TestUpsertMinerIfNew(t *testing.T) {
	s := openTestStore(t)

	disco := base.Miner{
		ID:     "bitaxe-192-168-1-45",
		IP:     "192.168.1.45",
		Model:  "Bitaxe (auto)",
		Driver: "bitaxe",
	}
	if err := s.UpsertMinerIfNew(disco); err != nil {
		t.Fatalf("UpsertMinerIfNew (insert): %v", err)
	}
	minters, err := s.Miners()
	if err != nil || len(minters) != 1 || minters[0].ID != disco.ID {
		t.Fatalf("insert failed: %v %+v", err, minters)
	}

	// A second insert with different identity must NOT overwrite: the poller
	// owns the enriched fields once a miner is known.
	changed := disco
	changed.IP = "192.168.99.99"
	changed.Model = "Bitaxe (auto)"
	if err := s.UpsertMinerIfNew(changed); err != nil {
		t.Fatalf("UpsertMinerIfNew (existing): %v", err)
	}
	got, err := s.Miners()
	if err != nil || len(got) != 1 {
		t.Fatalf("want still 1 miner: %v %+v", err, got)
	}
	if got[0].IP != "192.168.1.45" {
		t.Fatalf("discovery overwrote a known miner: %+v", got[0])
	}
}

func TestLatestStats(t *testing.T) {
	s := openTestStore(t)

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
	older := st
	older.HashRateGH = 440
	older.Timestamp = st.Timestamp.Add(-time.Hour)

	if err := s.RecordStats("miner-abc", older); err != nil {
		t.Fatalf("RecordStats older: %v", err)
	}
	if err := s.RecordStats("miner-abc", st); err != nil {
		t.Fatalf("RecordStats: %v", err)
	}

	got, err := s.LatestStats("miner-abc")
	if err != nil {
		t.Fatalf("LatestStats: %v", err)
	}
	if got == nil {
		t.Fatal("LatestStats: want one point, got nil")
	}
	if *got != st {
		t.Errorf("LatestStats: got %+v, want %+v", *got, st)
	}

	// Unknown miner: no error, nil point.
	got, err = s.LatestStats("no-such-miner")
	if err != nil {
		t.Fatalf("LatestStats unknown: %v", err)
	}
	if got != nil {
		t.Errorf("unknown miner: want nil, got %+v", *got)
	}
}

func TestRecordStatsAndHistory(t *testing.T) {
	s := openTestStore(t)

	st1 := base.Stats{
		HashRateGH:   450.2,
		Accepted:     100,
		Rejected:     2,
		TemperatureC: 58.4,
		FanSpeed:     72,
		PowerW:       20.86,
		Uptime:       90 * time.Second,
		Timestamp:    time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC),
	}
	st2 := st1
	st2.Timestamp = st1.Timestamp.Add(time.Minute)
	st2.HashRateGH = 455.0

	if err := s.RecordStats("miner-abc", st1); err != nil {
		t.Fatalf("RecordStats: %v", err)
	}
	if err := s.RecordStats("miner-abc", st2); err != nil {
		t.Fatalf("RecordStats: %v", err)
	}

	// Both points, oldest first.
	hist, err := s.History("miner-abc", st1.Timestamp)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist) != 2 {
		t.Fatalf("want 2 points, got %d", len(hist))
	}
	if hist[0] != st1 || hist[1] != st2 {
		t.Errorf("roundtrip mismatch:\n got %+v\nwant %+v", hist, []base.Stats{st1, st2})
	}

	// Since a later cutoff: only the second point.
	since := st1.Timestamp.Add(time.Second)
	hist, err = s.History("miner-abc", since)
	if err != nil {
		t.Fatalf("History (cutoff): %v", err)
	}
	if len(hist) != 1 || hist[0] != st2 {
		t.Errorf("cutoff query: want only %+v, got %+v", st2, hist)
	}
}
