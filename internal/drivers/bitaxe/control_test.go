package bitaxe

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gregweir/homeHash/internal/drivers/base"
)

// recordingMiner is a fake axeOS device that records requests so control
// (write) behavior can be asserted.
type recordingMiner struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []recordedCall
}

type recordedCall struct {
	method string
	uri    string
	body   []byte
}

func newRecordingMiner(t *testing.T) (rm *recordingMiner, ip string) {
	t.Helper()
	rm = &recordingMiner{}
	rm.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rm.mu.Lock()
		rm.calls = append(rm.calls, recordedCall{method: r.Method, uri: r.RequestURI, body: body})
		rm.mu.Unlock()
		// The firmware answers success with an empty JSON object; writes to
		// settings that fail validation return 400.
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch && len(rm.calls) > 0 {
			var patch map[string]float64
			json.Unmarshal(body, &patch)
			if v, ok := patch["frequency"]; ok && (v <= 0 || v > 1000) {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error": "value out of range"}`))
				return
			}
		}
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(rm.srv.Close)
	return rm, rm.srv.Listener.Addr().String()
}

func (rm *recordingMiner) snapshot() []recordedCall {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	out := make([]recordedCall, len(rm.calls))
	copy(out, rm.calls)
	return out
}

func TestApplySettingsSendsPatch(t *testing.T) {
	rm, ip := newRecordingMiner(t)
	d := New()
	m := minerAt(ip)

	freq := 620.5
	if err := d.ApplySettings(m, base.SettingsPatch{Frequency: &freq}); err != nil {
		t.Fatalf("ApplySettings: %v", err)
	}

	calls := rm.snapshot()
	if len(calls) != 1 {
		t.Fatalf("want 1 request, got %d", len(calls))
	}
	c := calls[0]
	if c.method != http.MethodPatch || c.uri != "/api/system" {
		t.Fatalf("request = %s %s, want PATCH /api/system", c.method, c.uri)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(c.body, &body); err != nil {
		t.Fatalf("patch body not JSON: %q", c.body)
	}
	if v, ok := body["frequency"].(float64); !ok || v != 620.5 {
		t.Fatalf("patch body = %s, want only frequency 620.5", c.body)
	}
	if len(body) != 1 {
		t.Fatalf("patch body = %s, want only the provided key", c.body)
	}
}

func TestApplySettingsVoltageAndBoth(t *testing.T) {
	rm, ip := newRecordingMiner(t)
	d := New()
	m := minerAt(ip)

	v := 1160
	if err := d.ApplySettings(m, base.SettingsPatch{CoreVoltage: &v}); err != nil {
		t.Fatalf("ApplySettings (voltage): %v", err)
	}
	freq := 618.0
	if err := d.ApplySettings(m, base.SettingsPatch{Frequency: &freq, CoreVoltage: &v}); err != nil {
		t.Fatalf("ApplySettings (both): %v", err)
	}

	calls := rm.snapshot()
	if len(calls) != 2 {
		t.Fatalf("want 2 requests, got %d", len(calls))
	}
	var vBody, bothBody map[string]interface{}
	json.Unmarshal(calls[0].body, &vBody)
	json.Unmarshal(calls[1].body, &bothBody)
	if len(vBody) != 1 || vBody["coreVoltage"] != float64(1160) {
		t.Fatalf("voltage patch = %s", calls[0].body)
	}
	if len(bothBody) != 2 || bothBody["coreVoltage"] != float64(1160) || bothBody["frequency"] != 618.0 {
		t.Fatalf("combined patch = %s", calls[1].body)
	}
}

func TestApplySettingsEmptyPatchSendsNothing(t *testing.T) {
	rm, ip := newRecordingMiner(t)
	d := New()

	if err := d.ApplySettings(minerAt(ip), base.SettingsPatch{}); err != nil {
		t.Fatalf("ApplySettings on empty patch: %v", err)
	}
	if n := len(rm.snapshot()); n != 0 {
		t.Fatalf("empty patch sent %d requests, want 0", n)
	}
}

func TestApplySettingsDeviceErrorSurfaces(t *testing.T) {
	_, ip := newRecordingMiner(t)
	d := New()

	bad := 2000.0
	err := d.ApplySettings(minerAt(ip), base.SettingsPatch{Frequency: &bad})
	if err == nil {
		t.Fatal("3000 MHz accepted, want device 400 to surface as error")
	}
}

func TestPauseAndResumePosts(t *testing.T) {
	rm, ip := newRecordingMiner(t)
	d := New()
	m := minerAt(ip)

	if err := d.Pause(m); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := d.Resume(m); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	calls := rm.snapshot()
	if len(calls) != 2 {
		t.Fatalf("want 2 requests, got %d", len(calls))
	}
	if calls[0].method != http.MethodPost || calls[0].uri != "/api/system/pause" {
		t.Fatalf("pause = %s %s", calls[0].method, calls[0].uri)
	}
	if calls[1].method != http.MethodPost || calls[1].uri != "/api/system/resume" {
		t.Fatalf("resume = %s %s", calls[1].method, calls[1].uri)
	}
}
