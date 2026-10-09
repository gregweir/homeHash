package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gregweir/homeHash/internal/drivers/base"
	"github.com/gregweir/homeHash/internal/store"
)

/* ---------- helpers ---------- */

type recorder struct {
	mu     sync.Mutex
	events []Alert
	after  func()
}

func (r *recorder) Notify(_ context.Context, a Alert) error {
	r.mu.Lock()
	r.events = append(r.events, a)
	r.mu.Unlock()
	if r.after != nil {
		r.after()
	}
	return nil
}

func (r *recorder) all() []Alert {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Alert(nil), r.events...)
}

func fixedNow() func() time.Time {
	return func() time.Time { return time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC) }
}

func evalWith(now func() time.Time) *Evaluator {
	return NewEvaluator(Config{
		TempWarn:     75,
		TempCritical: 85,
		OfflineAfter: 5 * time.Minute,
		Cooldown:     15 * time.Minute,
		Now:          now,
	})
}

func onlineMiner(now time.Time) base.Miner {
	return base.Miner{ID: "m1", IP: "192.168.0.10", Model: "Gamma 601", LastSeen: now}
}

/* ---------- evaluator: offline / recovery ---------- */

func TestEvaluatorAlertsOfflineOnceThenRecovers(t *testing.T) {
	now := fixedNow()
	e := evalWith(now)

	miner := onlineMiner(now().Add(-10 * time.Minute)) // stale: last poll 10m ago

	alerts := e.Evaluate(miner, nil)
	if len(alerts) != 1 || alerts[0].Kind != KindOffline {
		t.Fatalf("alerts = %+v, want one offline alert", alerts)
	}
	if alerts[0].Level != LevelCritical || alerts[0].MinerID != "m1" || alerts[0].MinerIP != "192.168.0.10" {
		t.Fatalf("offline alert = %+v", alerts[0])
	}

	// A second evaluation must not repeat the same alert.
	if alerts := e.Evaluate(miner, nil); len(alerts) != 0 {
		t.Fatalf("repeat alert: %+v", alerts)
	}

	// Back online → one recovery alert.
	miner = onlineMiner(now())
	recov := e.Evaluate(miner, &base.Stats{TemperatureC: 55})
	if len(recov) != 1 || recov[0].Kind != KindRecovered {
		t.Fatalf("recovery alerts = %+v, want one", recov)
	}
	if recov[0].Level != LevelInfo {
		t.Fatalf("recovery level = %q, want info", recov[0].Level)
	}
	// ...and not again.
	if alerts := e.Evaluate(miner, &base.Stats{TemperatureC: 55}); len(alerts) != 0 {
		t.Fatalf("repeat recovery: %+v", alerts)
	}
}

func TestEvaluatorIgnoresNewMinersWithoutData(t *testing.T) {
	e := evalWith(fixedNow())
	// Never polled: zero LastSeen, no stats.
	alerts := e.Evaluate(base.Miner{ID: "m1", IP: "192.168.0.10"}, nil)
	if len(alerts) != 0 {
		t.Fatalf("alerts = %+v, want none for a fresh miner", alerts)
	}
}

/* ---------- evaluator: temperature ---------- */

func TestEvaluatorTemperatureEscalationAndRecovery(t *testing.T) {
	now := fixedNow()
	e := evalWith(now)
	miner := onlineMiner(now())

	// 80°C → one warning.
	alerts := e.Evaluate(miner, &base.Stats{TemperatureC: 80})
	if len(alerts) != 1 || alerts[0].Kind != KindTemperature || alerts[0].Level != LevelWarning {
		t.Fatalf("alerts = %+v, want one temperature warning", alerts)
	}

	// 90°C → escalates to critical.
	alerts = e.Evaluate(miner, &base.Stats{TemperatureC: 90})
	if len(alerts) != 1 || alerts[0].Level != LevelCritical {
		t.Fatalf("alerts = %+v, want critical escalation", alerts)
	}

	// Back to 60°C → recovery.
	alerts = e.Evaluate(miner, &base.Stats{TemperatureC: 60})
	if len(alerts) != 1 || alerts[0].Kind != KindTempRecovered {
		t.Fatalf("alerts = %+v, want temperature recovery", alerts)
	}
	if alerts := e.Evaluate(miner, &base.Stats{TemperatureC: 60}); len(alerts) != 0 {
		t.Fatalf("repeat recovery: %+v", alerts)
	}
}

func TestEvaluatorTemperatureRespectsCooldown(t *testing.T) {
	start := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)
	cur := start
	e := evalWith(func() time.Time { return cur })

	if alerts := e.Evaluate(onlineMiner(cur), &base.Stats{TemperatureC: 80}); len(alerts) != 1 {
		t.Fatalf("want first warning, got %+v", alerts)
	}
	cur = cur.Add(5 * time.Minute) // inside the 15m cooldown
	if alerts := e.Evaluate(onlineMiner(cur), &base.Stats{TemperatureC: 80}); len(alerts) != 0 {
		t.Fatalf("repeat warning inside cooldown: %+v", alerts)
	}
	cur = cur.Add(15 * time.Minute) // beyond cooldown: nudge again
	alerts := e.Evaluate(onlineMiner(cur), &base.Stats{TemperatureC: 80})
	if len(alerts) != 1 || alerts[0].Kind != KindTemperature {
		t.Fatalf("alerts = %+v, want repeat warning after cooldown", alerts)
	}
}

func TestEvaluatorTemperatureClearsWhenMinerGoesOffline(t *testing.T) {
	now := fixedNow()
	e := evalWith(now)
	miner := onlineMiner(now())

	if alerts := e.Evaluate(miner, &base.Stats{TemperatureC: 90}); len(alerts) != 1 {
		t.Fatalf("want critical alert, got %+v", alerts)
	}

	// Miner dies: no fresh stats; the temp alert state clears silently.
	stale := onlineMiner(now().Add(-10 * time.Minute))
	for _, a := range e.Evaluate(stale, nil) {
		if a.Kind == KindTempRecovered {
			t.Fatalf("unexpected temp recovery on offline: %+v", a)
		}
	}

	// When it comes back, the offline recovery alert fires, but not a
	// temperature recovery (the temp state was already cleared).
	fresh := onlineMiner(now())
	alerts := e.Evaluate(fresh, &base.Stats{TemperatureC: 50})
	for _, a := range alerts {
		if a.Kind == KindTempRecovered {
			t.Fatalf("unexpected temp recovery: %+v", a)
		}
	}
	if len(alerts) != 1 || alerts[0].Kind != KindRecovered {
		t.Fatalf("alerts = %+v, want one offline recovery", alerts)
	}
}

/* ---------- run loop ---------- */

func TestRunLoopNotifiesOnlineMinerAlerts(t *testing.T) {
	st := openTestStore(t)

	now := time.Now()
	if err := st.UpsertMiner(onlineMiner(now)); err != nil {
		t.Fatalf("UpsertMiner: %v", err)
	}
	if err := st.RecordStats("m1", base.Stats{TemperatureC: 90, Timestamp: now}); err != nil {
		t.Fatalf("RecordStats: %v", err)
	}

	rec := &recorder{}
	done := make(chan struct{})
	rec.after = func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, st, evalWith(func() time.Time { return time.Now() }), rec, 10*time.Millisecond)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("no alert within 2s")
	}
}

func TestRunLoopStopsOnContextCancel(t *testing.T) {
	st := openTestStore(t)
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Run(ctx, st, evalWith(func() time.Time { return time.Now() }), rec, time.Hour); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit on ctx cancel")
	}
}

/* ---------- notifiers ---------- */

func TestDiscordWebhookPostsContent(t *testing.T) {
	var mu sync.Mutex
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	wh := NewDiscordWebhook(srv.URL, 2*time.Second)
	if err := wh.Notify(context.Background(), Alert{
		Level: LevelWarning, Model: "Gamma 601", MinerIP: "192.168.0.10",
		Message: "temperature 76.3°C",
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("got %d payloads, want 1", len(got))
	}
	content, _ := got[0]["content"].(string)
	if content == "" {
		t.Fatalf("payload has no content: %v", got[0])
	}
}

func TestDiscordWebhookReturnsErrorOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"missing permissions"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	wh := NewDiscordWebhook(srv.URL, 2*time.Second)
	err := wh.Notify(context.Background(), Alert{Level: LevelWarning, Message: "x"})
	if err == nil {
		t.Fatal("Notify succeeded on HTTP 403, want error")
	}
}

func TestLogNotifierWritesToLog(t *testing.T) {
	ln := LogNotifier{}
	if err := ln.Notify(context.Background(), Alert{Message: "hello"}); err != nil {
		t.Fatalf("LogNotifier: %v", err)
	}
}

var errTest = errors.New("test")

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
