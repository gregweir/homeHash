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
}

func TestAlertConfigFromEnv(t *testing.T) {
	set := func(k, v string) { t.Setenv(k, v) }
	set("HOMEHASH_ALERT_WEBHOOK_URL", "https://discord.example/api/webhooks/1/x")
	set("HOMEHASH_ALERT_TEMP_WARN", "70.5")
	set("HOMEHASH_ALERT_TEMP_CRITICAL", "88")
	set("HOMEHASH_ALERT_OFFLINE_AFTER", "3m")
	set("HOMEHASH_ALERT_COOLDOWN", "120")
	set("HOMEHASH_ALERT_INTERVAL", "30s")
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
	if len(cfg.Miners) != 2 || cfg.Miners[1] != "192.168.1.11" {
		t.Fatalf("Miners = %v", cfg.Miners)
	}
}
