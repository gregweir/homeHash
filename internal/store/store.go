// Package store persists miners and stat history in a local SQLite database.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver; registers "sqlite"

	"github.com/gregweir/homeHash/internal/drivers/base"
)

// Store wraps the SQLite database holding miner inventory and stats history.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the database at path and ensures the schema.
func Open(path string) (*Store, error) {
	// The server reads history/SSE snapshots constantly while the poller writes;
	// WAL + a busy timeout keep those readers and writers from surfacing
	// SQLITE_BUSY. DSN pragmas apply to every pooled connection.
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	schema := `
		CREATE TABLE IF NOT EXISTS miners (
			id        TEXT PRIMARY KEY,
			ip        TEXT NOT NULL,
			model     TEXT,
			driver    TEXT NOT NULL,
			pool_url  TEXT,
			firmware  TEXT,
			mac       TEXT,
			last_seen TEXT
		);
		CREATE TABLE IF NOT EXISTS stats_history (
			miner_id    TEXT NOT NULL,
			hash_rate_gh REAL,
			accepted    INTEGER,
			rejected    INTEGER,
			temperature_c REAL,
			fan_speed   INTEGER,
			power_w     REAL,
			uptime_nanos INTEGER,
			timestamp   TEXT NOT NULL,
			FOREIGN KEY (miner_id) REFERENCES miners(id)
		);
		CREATE INDEX IF NOT EXISTS idx_stats_history_miner_ts
			ON stats_history(miner_id, timestamp);
		CREATE TABLE IF NOT EXISTS alerts (
			id        INTEGER PRIMARY KEY AUTOINCREMENT,
			time      TEXT NOT NULL,
			miner_id  TEXT,
			miner_ip  TEXT,
			model     TEXT,
			kind      TEXT NOT NULL,
			level     TEXT NOT NULL,
			message   TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_alerts_time ON alerts(time DESC);
	`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := addColumnIfMissing(db, "miners", "mac", "TEXT"); err != nil {
		db.Close()
		return nil, err
	}
	if err := addColumnIfMissing(db, "stats_history", "power_w", "REAL"); err != nil {
		db.Close()
		return nil, err
	}
	if err := addColumnIfMissing(db, "alerts", "miner_ip", "TEXT"); err != nil {
		db.Close()
		return nil, err
	}
	if err := addColumnIfMissing(db, "alerts", "model", "TEXT"); err != nil {
		db.Close()
		return nil, err
	}
	for _, col := range []struct{ name, typ string }{
		{"frequency_mhz", "REAL"},
		{"core_voltage_mv", "INTEGER"},
		{"paused", "INTEGER"},
		{"actual_frequency_mhz", "REAL"},
		{"actual_core_voltage_mv", "INTEGER"},
	} {
		if err := addColumnIfMissing(db, "stats_history", col.name, col.typ); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

// addColumnIfMissing migrates databases created before a column was added.
func addColumnIfMissing(db *sql.DB, table, col, typ string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()

	exists := false
	for rows.Next() {
		var (
			cid     int
			name    string
			colTyp  string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &colTyp, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == col {
			exists = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !exists {
		if _, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + col + " " + typ); err != nil {
			return fmt.Errorf("migrate %s.%s: %w", table, col, err)
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// UpsertMiner inserts or updates a miner, keyed by ID.
func (s *Store) UpsertMiner(m base.Miner) error {
	_, err := s.db.Exec(`
		INSERT INTO miners (id, ip, model, driver, pool_url, firmware, mac, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			ip = excluded.ip,
			model = excluded.model,
			driver = excluded.driver,
			pool_url = excluded.pool_url,
			firmware = excluded.firmware,
			mac = excluded.mac,
			last_seen = excluded.last_seen`,
		m.ID, m.IP, m.Model, m.Driver, m.PoolURL, m.Firmware, m.MAC,
		m.LastSeen.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("upsert miner %s: %w", m.ID, err)
	}
	return nil
}

// UpsertMinerIfNew inserts a miner only when its ID is unknown, ignoring the
// write otherwise. Discovery uses this: it may add miners the poller hasn't
// seen yet, but must never stomp the poller-enriched identity of a known one.
func (s *Store) UpsertMinerIfNew(m base.Miner) error {
	_, err := s.db.Exec(`
		INSERT INTO miners (id, ip, model, driver, pool_url, firmware, mac, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		m.ID, m.IP, m.Model, m.Driver, m.PoolURL, m.Firmware, m.MAC,
		m.LastSeen.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("upsert-if-new miner %s: %w", m.ID, err)
	}
	return nil
}

// GetMiner returns one miner by ID, or nil if unknown.
func (s *Store) GetMiner(id string) (*base.Miner, error) {
	all, err := s.Miners()
	if err != nil {
		return nil, err
	}
	for _, m := range all {
		if m.ID == id {
			return &m, nil
		}
	}
	return nil, nil
}

// Miners returns all known miners, ordered by ID.
func (s *Store) Miners() ([]base.Miner, error) {
	rows, err := s.db.Query(`
		SELECT id, ip, COALESCE(model, ''), driver,
		       COALESCE(pool_url, ''), COALESCE(firmware, ''),
		       COALESCE(mac, ''), COALESCE(last_seen, '')
		FROM miners ORDER BY id`)

	if err != nil {
		return nil, fmt.Errorf("list miners: %w", err)
	}
	defer rows.Close()

	var out []base.Miner
	for rows.Next() {
		var (
			m       base.Miner
			lastStr string
		)
		if err := rows.Scan(&m.ID, &m.IP, &m.Model, &m.Driver,
			&m.PoolURL, &m.Firmware, &m.MAC, &lastStr); err != nil {
			return nil, fmt.Errorf("scan miner: %w", err)
		}
		if lastStr != "" {
			m.LastSeen, err = time.Parse(time.RFC3339Nano, lastStr)
			if err != nil {
				return nil, fmt.Errorf("parse last_seen for %s: %w", m.ID, err)
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RecordStats appends one poll result to the miner's history.
func (s *Store) RecordStats(minerID string, st base.Stats) error {
	_, err := s.db.Exec(`
		INSERT INTO stats_history
			(miner_id, hash_rate_gh, accepted, rejected, temperature_c, fan_speed, power_w, uptime_nanos, frequency_mhz, core_voltage_mv, paused, actual_frequency_mhz, actual_core_voltage_mv, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		minerID, st.HashRateGH, st.Accepted, st.Rejected,
		st.TemperatureC, st.FanSpeed, st.PowerW, st.Uptime.Nanoseconds(),
		st.Frequency, st.CoreVoltageMv, btoi(st.Paused),
		st.ActualFrequency, st.CoreVoltageActualMv,
		st.Timestamp.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record stats for %s: %w", minerID, err)
	}
	return nil
}

// LatestStats returns the most recent stat point for a miner, nil if none.
func (s *Store) LatestStats(minerID string) (*base.Stats, error) {
	row := s.db.QueryRow(`
		SELECT hash_rate_gh, accepted, rejected, temperature_c, fan_speed,
		       power_w, uptime_nanos, timestamp,
		       COALESCE(frequency_mhz, 0), COALESCE(core_voltage_mv, 0), COALESCE(paused, 0),
		       COALESCE(actual_frequency_mhz, 0), COALESCE(actual_core_voltage_mv, 0)
		FROM stats_history
		WHERE miner_id = ?
		ORDER BY timestamp DESC
		LIMIT 1`, minerID)

	var (
		st      base.Stats
		upNanos int64
		tsStr   string
		pausedI int64
	)
	err := row.Scan(&st.HashRateGH, &st.Accepted, &st.Rejected,
		&st.TemperatureC, &st.FanSpeed, &st.PowerW, &upNanos, &tsStr,
		&st.Frequency, &st.CoreVoltageMv, &pausedI,
		&st.ActualFrequency, &st.CoreVoltageActualMv)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("latest stats for %s: %w", minerID, err)
	}
	st.Paused = pausedI != 0
	st.Uptime = time.Duration(upNanos)
	st.Timestamp, err = time.Parse(time.RFC3339Nano, tsStr)
	if err != nil {
		return nil, fmt.Errorf("parse timestamp: %w", err)
	}
	return &st, nil
}

// AddAlert persists one alert. Alerts are append-only history: the loop
// writes them as they fire, and the dashboard serves the newest first.
func (s *Store) AddAlert(a base.Alert) error {
	if a.Time.IsZero() {
		a.Time = time.Now()
	}
	_, err := s.db.Exec(`
		INSERT INTO alerts (time, miner_id, miner_ip, model, kind, level, message)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.Time.Format(time.RFC3339Nano), a.MinerID, a.MinerIP, a.Model,
		a.Kind, a.Level, a.Message)
	if err != nil {
		return fmt.Errorf("add alert: %w", err)
	}
	return nil
}

// ListAlerts returns up to limit alerts, newest first.
func (s *Store) ListAlerts(limit int) ([]base.Alert, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT time, kind, level,
		       COALESCE(miner_id, ''), COALESCE(miner_ip, ''),
		       COALESCE(model, ''), COALESCE(message, '')
		FROM alerts ORDER BY time DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()

	var out []base.Alert
	for rows.Next() {
		var (
			a       base.Alert
			timeStr string
		)
		if err := rows.Scan(&timeStr, &a.Kind, &a.Level,
			&a.MinerID, &a.MinerIP, &a.Model, &a.Message); err != nil {
			return nil, fmt.Errorf("scan alert: %w", err)
		}
		a.Time, err = time.Parse(time.RFC3339Nano, timeStr)
		if err != nil {
			return nil, fmt.Errorf("parse alert time: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// btoi encodes a bool for an INTEGER column.
func btoi(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// History returns a miner's stat points recorded at or after since, oldest first.
func (s *Store) History(minerID string, since time.Time) ([]base.Stats, error) {
	rows, err := s.db.Query(`
		SELECT hash_rate_gh, accepted, rejected, temperature_c, fan_speed,
		       power_w, uptime_nanos, timestamp,
		       COALESCE(frequency_mhz, 0), COALESCE(core_voltage_mv, 0), COALESCE(paused, 0),
		       COALESCE(actual_frequency_mhz, 0), COALESCE(actual_core_voltage_mv, 0)
		FROM stats_history
		WHERE miner_id = ? AND timestamp >= ?
		ORDER BY timestamp ASC`,
		minerID, since.Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("history for %s: %w", minerID, err)
	}
	defer rows.Close()

	var out []base.Stats
	for rows.Next() {
		var (
			st      base.Stats
			upNanos int64
			tsStr   string
			pausedI int64
		)
		if err := rows.Scan(&st.HashRateGH, &st.Accepted, &st.Rejected,
			&st.TemperatureC, &st.FanSpeed, &st.PowerW, &upNanos, &tsStr,
			&st.Frequency, &st.CoreVoltageMv, &pausedI,
			&st.ActualFrequency, &st.CoreVoltageActualMv); err != nil {
			return nil, fmt.Errorf("scan stats: %w", err)
		}
		st.Paused = pausedI != 0
		st.Uptime = time.Duration(upNanos)
		st.Timestamp, err = time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			return nil, fmt.Errorf("parse timestamp: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
