package config

import (
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := Default()
	if cfg.Port == 0 || cfg.DatabasePath == "" || cfg.PollInterval == 0 {
		t.Fatalf("defaults missing: %+v", cfg)
	}
}

func TestAlertConfigDefaults(t *testing.T) {
	cfg := Default()
	if !cfg.AlertEnabled {
		t.Fatal("alerts should default to enabled")
	}
	if cfg.AlertWebhookURL == "" {
		// No webhook by default: log-only alerting.
		t.Logf("webhook unset by default (ok): %q", cfg.AlertWebhookURL)
	}
	if cfg.TempWarningC != 75 {
		t.Fatalf("TempWarningC = %v, want 75", cfg.TempWarningC)
	}
	if cfg.TempCriticalC != 85 {
		t.Fatalf("TempCriticalC = %v, want 85", cfg.TempCriticalC)
	}
	if cfg.OfflineAfter != 10*time.Minute {
		t.Fatalf("OfflineAfter = %v, want 10m", cfg.OfflineAfter)
	}
	if cfg.AlertCooldown != 15*time.Minute {
		t.Fatalf("AlertCooldown = %v, want 15m", cfg.AlertCooldown)
	}
	if cfg.AlertInterval <= 0 {
		t.Fatalf("AlertInterval = %v, want positive", cfg.AlertInterval)
	}
	// Rejected-share alert: warn above 5% lifetime rejection, opt-out with 0.
	if cfg.ShareErrorWarn != 5 {
		t.Fatalf("ShareErrorWarn = %v, want 5", cfg.ShareErrorWarn)
	}
}

func TestAlertConfigFromEnv(t *testing.T) {
	set := func(k, v string) { t.Setenv(k, v) }
	set("HOMEHASH_ALERT_WEBHOOK_URL", "https://discord.example/api/webhooks/1/x")
	set("HOMEHASH_ALERT_TEMP_WARN", "70.5")
	set("HOMEHASH_ALERT_TEMP_CRITICAL", "88")
	set("HOMEHASH_ALERT_OFFLINE_AFTER", "3m")
	set("HOMEHASH_ALERT_COOLDOWN", "120")
	set("HOMEHASH_ALERT_INTERVAL", "30s")
	set("HOMEHASH_ALERT_SHARE_ERR", "7.5")
	set("HOMEHASH_MINER", "192.168.1.10,192.168.1.11")

	cfg := Default()
	if cfg.AlertWebhookURL != "https://discord.example/api/webhooks/1/x" {
		t.Fatalf("webhook = %q", cfg.AlertWebhookURL)
	}
	if cfg.TempWarningC != 70.5 {
		t.Fatalf("TempWarningC = %v, want 70.5", cfg.TempWarningC)
	}
	if cfg.TempCriticalC != 88 {
		t.Fatalf("TempCriticalC = %v, want 88", cfg.TempCriticalC)
	}
	if cfg.OfflineAfter != 3*time.Minute {
		t.Fatalf("OfflineAfter = %v, want 3m", cfg.OfflineAfter)
	}
	if cfg.AlertCooldown != 2*time.Minute {
		t.Fatalf("AlertCooldown = %v, want 120s", cfg.AlertCooldown)
	}
	if cfg.AlertInterval != 30*time.Second {
		t.Fatalf("AlertInterval = %v, want 30s", cfg.AlertInterval)
	}
	if cfg.ShareErrorWarn != 7.5 {
		t.Fatalf("ShareErrorWarn = %v, want 7.5", cfg.ShareErrorWarn)
	}
	if len(cfg.Miners) != 2 || cfg.Miners[1] != "192.168.1.11" {
		t.Fatalf("Miners = %v", cfg.Miners)
	}
}

func TestShareErrorWarnZeroDisables(t *testing.T) {
	t.Setenv("HOMEHASH_ALERT_SHARE_ERR", "0")
	if cfg := Default(); cfg.ShareErrorWarn != 0 {
		t.Fatalf("ShareErrorWarn = %v, want 0 (disabled)", cfg.ShareErrorWarn)
	}
}

func TestEmailConfigDefaults(t *testing.T) {
	cfg := Default()
	if cfg.AlertEmailHost != "" || cfg.AlertEmailFrom != "" || len(cfg.AlertEmailTo) != 0 {
		t.Fatalf("email should default off, got host=%q to=%v", cfg.AlertEmailHost, cfg.AlertEmailTo)
	}
	if cfg.AlertEmailPort != 587 {
		t.Fatalf("AlertEmailPort = %d, want 587 (STARTTLS)", cfg.AlertEmailPort)
	}
}

func TestEmailConfigFromEnv(t *testing.T) {
	t.Setenv("HOMEHASH_ALERT_EMAIL_HOST", "smtp.home.local")
	t.Setenv("HOMEHASH_ALERT_EMAIL_PORT", "465")
	t.Setenv("HOMEHASH_ALERT_EMAIL_USERNAME", "hash")
	t.Setenv("HOMEHASH_ALERT_EMAIL_PASSWORD", "s3cret")
	t.Setenv("HOMEHASH_ALERT_EMAIL_FROM", "hash@home.local")
	t.Setenv("HOMEHASH_ALERT_EMAIL_TO", "greg@home.local,backup@home.local")

	cfg := Default()
	if cfg.AlertEmailHost != "smtp.home.local" {
		t.Fatalf("AlertEmailHost = %q", cfg.AlertEmailHost)
	}
	if cfg.AlertEmailPort != 465 {
		t.Fatalf("AlertEmailPort = %d", cfg.AlertEmailPort)
	}
	if cfg.AlertEmailUsername != "hash" || cfg.AlertEmailPassword != "s3cret" {
		t.Fatalf("email credentials = %q/%q", cfg.AlertEmailUsername, cfg.AlertEmailPassword)
	}
	if cfg.AlertEmailFrom != "hash@home.local" {
		t.Fatalf("AlertEmailFrom = %q", cfg.AlertEmailFrom)
	}
	if len(cfg.AlertEmailTo) != 2 || cfg.AlertEmailTo[1] != "backup@home.local" {
		t.Fatalf("AlertEmailTo = %v", cfg.AlertEmailTo)
	}
}

func TestControlDefaultsOff(t *testing.T) {
	cfg := Default()

	// Write access must be opt-in: a monitor ships read-only.
	if cfg.ControlEnabled {
		t.Fatal("control should default to disabled")
	}
	if cfg.ControlFreqMin != 100 || cfg.ControlFreqMax != 750 {
		t.Fatalf("freq clamp = [%v, %v]", cfg.ControlFreqMin, cfg.ControlFreqMax)
	}
	if cfg.ControlVoltMin != 900 || cfg.ControlVoltMax != 1300 {
		t.Fatalf("voltage clamp = [%d, %d] mV", cfg.ControlVoltMin, cfg.ControlVoltMax)
	}
}

func TestControlFromEnv(t *testing.T) {
	t.Setenv("HOMEHASH_CONTROL", "true")
	t.Setenv("HOMEHASH_CONTROL_FREQ_MIN", "200")
	t.Setenv("HOMEHASH_CONTROL_FREQ_MAX", "700")
	t.Setenv("HOMEHASH_CONTROL_VOLT_MIN", "950")
	t.Setenv("HOMEHASH_CONTROL_VOLT_MAX", "1250")

	cfg := Default()
	if !cfg.ControlEnabled {
		t.Fatal("HOMEHASH_CONTROL=true ignored")
	}
	if cfg.ControlFreqMin != 200 || cfg.ControlFreqMax != 700 {
		t.Fatalf("freq clamp from env = [%v, %v]", cfg.ControlFreqMin, cfg.ControlFreqMax)
	}
	if cfg.ControlVoltMin != 950 || cfg.ControlVoltMax != 1250 {
		t.Fatalf("voltage clamp from env = [%d, %d]", cfg.ControlVoltMin, cfg.ControlVoltMax)
	}
}
