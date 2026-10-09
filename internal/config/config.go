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
	}
}

// getMiners parses the HOMEHASH_MINER seed list.
func getMiners() []string {
	ips := os.Getenv("HOMEHASH_MINER")
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
