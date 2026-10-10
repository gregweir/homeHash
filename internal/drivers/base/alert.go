package base

import "time"

// AlertKind names the condition an alert reports.
type AlertKind string

const (
	KindOffline      AlertKind = "offline"
	KindTemperature  AlertKind = "temperature"
	KindRecovered    AlertKind = "recovered"
	KindTempRecovered AlertKind = "temp-recovered"
)

// AlertLevel is an alert's severity.
type AlertLevel string

const (
	LevelInfo     AlertLevel = "info"
	LevelWarning  AlertLevel = "warning"
	LevelCritical AlertLevel = "critical"
)

// Alert is one notification about one miner. Raised by the alerting loop,
// persisted by the store, served to the dashboard.
type Alert struct {
	Time    time.Time
	Kind    AlertKind
	Level   AlertLevel
	MinerID string
	MinerIP string
	Model   string
	Message string
}