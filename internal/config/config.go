// Package config handles configuration loading and defaults for homeHash.
// Configuration can be provided via environment variables or a config file.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration for homeHash.
type Config struct {
	Port              int           // HTTP server port
	DatabasePath      string        // SQLite database file path
	PollInterval      time.Duration // How often to poll miners
	DiscoveryEnabled  bool          // Enable mDNS/network discovery
	DiscoveryInterval time.Duration // How often to run discovery

	Miners []string // Seed miner IPs (or ip:port), from HOMEHASH_MINER

	AlertEnabled    bool          // Run the alerting loop
	AlertWebhookURL string        // Discord webhook; empty: log-only alerting
	TempWarningC    float64       // Warn above this board temperature
	TempCriticalC   float64       // Critical above this board temperature
	OfflineAfter    time.Duration // A miner is offline when last poll is older than this
	AlertCooldown   time.Duration // Re-nag for the same condition after this long
	AlertInterval   time.Duration // How often the alert loop evaluates
	// ShareErrorWarn is the lifetime rejected-share percentage above which a
	// miner is considered misconfigured or failing. Zero disables the alert.
	ShareErrorWarn float64
	// Email notifier (in addition to the webhook/log). All-or-nothing: host
	// set means email is on.
	AlertEmailHost     string // SMTP relay hostname; empty disables email
	AlertEmailPort     int    // 587 (STARTTLS), 465 (implicit TLS), 25
	AlertEmailUsername string // optional SMTP credentials
	AlertEmailPassword string
	AlertEmailFrom     string
	AlertEmailTo       []string // comma-separated list from HOMEHASH_ALERT_EMAIL_TO

	// Control: write access to miners (settings, pause, resume). Off by
	// default — a monitor ships read-only.
	ControlEnabled bool
	// Hard clamps on accepted values, overridable per environment. The
	// firmware itself would accept 1..65535 for both, which is no protection.
	ControlFreqMin, ControlFreqMax float64
	ControlVoltMin, ControlVoltMax int
}

// Default returns a Config with sensible defaults.
func Default() *Config {
	return &Config{
		Port:              getEnvInt("HOMEHASH_PORT", 8080),
		DatabasePath:      getEnv("HOMEHASH_DB", "./homehash.db"),
		PollInterval:      getEnvDuration("HOMEHASH_INTERVAL", 60*time.Second),
		DiscoveryEnabled:  getEnvBool("HOMEHASH_DISCOVERY", true),
		DiscoveryInterval: getEnvDuration("HOMEHASH_DISCOVERY_INTERVAL", 5*time.Minute),

		Miners: getMiners(),

		AlertEnabled:    getEnvBool("HOMEHASH_ALERTS", true),
		AlertWebhookURL: getEnv("HOMEHASH_ALERT_WEBHOOK_URL", ""),
		TempWarningC:    getEnvFloat("HOMEHASH_ALERT_TEMP_WARN", 75),
		TempCriticalC:   getEnvFloat("HOMEHASH_ALERT_TEMP_CRITICAL", 85),
		OfflineAfter:    getEnvDuration("HOMEHASH_ALERT_OFFLINE_AFTER", 10*time.Minute),
		AlertCooldown:   getEnvDuration("HOMEHASH_ALERT_COOLDOWN", 15*time.Minute),
		AlertInterval:   getEnvDuration("HOMEHASH_ALERT_INTERVAL", 30*time.Second),
		ShareErrorWarn:  getEnvFloat("HOMEHASH_ALERT_SHARE_ERR", 5),

		AlertEmailHost:     getEnv("HOMEHASH_ALERT_EMAIL_HOST", ""),
		AlertEmailPort:     getEnvInt("HOMEHASH_ALERT_EMAIL_PORT", 587),
		AlertEmailUsername: getEnv("HOMEHASH_ALERT_EMAIL_USERNAME", ""),
		AlertEmailPassword: getEnv("HOMEHASH_ALERT_EMAIL_PASSWORD", ""),
		AlertEmailFrom:     getEnv("HOMEHASH_ALERT_EMAIL_FROM", ""),
		AlertEmailTo:       getCommaList("HOMEHASH_ALERT_EMAIL_TO"),

		ControlEnabled: getEnvBool("HOMEHASH_CONTROL", false),
		ControlFreqMin: getEnvFloat("HOMEHASH_CONTROL_FREQ_MIN", 100),
		ControlFreqMax: getEnvFloat("HOMEHASH_CONTROL_FREQ_MAX", 750),
		ControlVoltMin: getEnvInt("HOMEHASH_CONTROL_VOLT_MIN", 900),
		ControlVoltMax: getEnvInt("HOMEHASH_CONTROL_VOLT_MAX", 1300),
	}
}

// getMiners parses the HOMEHASH_MINER seed list.
func getMiners() []string {
	return getCommaList("HOMEHASH_MINER")
}

// getCommaList parses a comma-separated env list, dropping blanks.
func getCommaList(key string) []string {
	ips := os.Getenv(key)
	if ips == "" {
		return nil
	}
	var out []string
	for _, ip := range strings.Split(ips, ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			out = append(out, ip)
		}
	}
	return out
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return defaultVal
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	// Fall back to a plain number, interpreted as seconds.
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return defaultVal
}

func getEnvFloat(key string, defaultVal float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return defaultVal
}
