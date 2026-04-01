package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq"
)

// DB wraps a Postgres connection.
type DB struct {
	conn *sql.DB
}

func New(dsn string) (*DB, error) {
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening db: %w", err)
	}
	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("pinging db: %w", err)
	}
	return &DB{conn: conn}, nil
}

// ---------------------------------------------------------------------------
// Migrations
// ---------------------------------------------------------------------------

func (d *DB) Migrate() error {
	_, err := d.conn.Exec(`
CREATE TABLE IF NOT EXISTS contracts (
    id               SERIAL PRIMARY KEY,
    salesforce_id    TEXT UNIQUE NOT NULL,
    account_name     TEXT,
    deal_name        TEXT,
    stage_name       TEXT,
    close_date       DATE,
    arr              NUMERIC(18,2) DEFAULT 0,
    delta_arr        NUMERIC(18,2) DEFAULT 0,
    currency_code    TEXT,
    last_modified_at TIMESTAMPTZ,
    synced_at        TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS sync_log (
    id          SERIAL PRIMARY KEY,
    synced_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    upserted    INTEGER,
    total       INTEGER,
    incremental BOOLEAN,
    error_msg   TEXT
);
`)
	return err
}

// ---------------------------------------------------------------------------
// Contract
// ---------------------------------------------------------------------------

// Contract mirrors the DB row.
type Contract struct {
	ID             int
	SalesforceID   string
	AccountName    string
	DealName       string
	StageName      string
	CloseDate      *time.Time
	ARR            float64
	DeltaARR       float64
	CurrencyCode   string
	LastModifiedAt time.Time
	SyncedAt       time.Time
}

// UpsertContracts inserts or updates a batch of contracts. Returns count upserted.
func (d *DB) UpsertContracts(contracts []Contract) (int, error) {
	if len(contracts) == 0 {
		return 0, nil
	}

	tx, err := d.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.Prepare(`
INSERT INTO contracts (
    salesforce_id, account_name, deal_name, stage_name,
    close_date, arr, delta_arr, currency_code,
    last_modified_at, synced_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NOW())
ON CONFLICT (salesforce_id) DO UPDATE SET
    account_name     = EXCLUDED.account_name,
    deal_name        = EXCLUDED.deal_name,
    stage_name       = EXCLUDED.stage_name,
    close_date       = EXCLUDED.close_date,
    arr              = EXCLUDED.arr,
    delta_arr        = EXCLUDED.delta_arr,
    currency_code    = EXCLUDED.currency_code,
    last_modified_at = EXCLUDED.last_modified_at,
    synced_at        = NOW()
`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for _, c := range contracts {
		_, err := stmt.Exec(
			c.SalesforceID, c.AccountName, c.DealName, c.StageName,
			c.CloseDate, c.ARR, c.DeltaARR, c.CurrencyCode,
			c.LastModifiedAt,
		)
		if err != nil {
			return 0, fmt.Errorf("upserting %s: %w", c.SalesforceID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(contracts), nil
}

// ListContracts returns all contracts. stageFilter: "CLOSED_WON" | "ALL"
func (d *DB) ListContracts(stageFilter string) ([]Contract, error) {
	q := `SELECT id, salesforce_id, account_name, deal_name, stage_name,
	             close_date, arr, delta_arr, currency_code, last_modified_at, synced_at
	      FROM contracts`
	if stageFilter != "" && stageFilter != "ALL" {
		q += " WHERE stage_name = 'Closed Won'"
	}
	q += " ORDER BY arr DESC NULLS LAST"

	rows, err := d.conn.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Contract
	for rows.Next() {
		var c Contract
		var closeDate sql.NullTime
		var lastMod sql.NullTime
		var synced sql.NullTime
		if err := rows.Scan(
			&c.ID, &c.SalesforceID, &c.AccountName, &c.DealName, &c.StageName,
			&closeDate, &c.ARR, &c.DeltaARR, &c.CurrencyCode,
			&lastMod, &synced,
		); err != nil {
			return nil, err
		}
		if closeDate.Valid {
			c.CloseDate = &closeDate.Time
		}
		if lastMod.Valid {
			c.LastModifiedAt = lastMod.Time
		}
		if synced.Valid {
			c.SyncedAt = synced.Time
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Summary
// ---------------------------------------------------------------------------

type Summary struct {
	TotalARR      float64
	TotalMRR      float64
	TotalDeltaARR float64
	ContractCount int
	LastSyncAt    *time.Time
}

func (d *DB) Summary() (*Summary, error) {
	var s Summary
	err := d.conn.QueryRow(`
		SELECT
			COALESCE(SUM(arr), 0),
			COALESCE(SUM(arr / 12), 0),
			COALESCE(SUM(delta_arr), 0),
			COUNT(*)
		FROM contracts
		WHERE stage_name = 'Closed Won'
	`).Scan(&s.TotalARR, &s.TotalMRR, &s.TotalDeltaARR, &s.ContractCount)
	if err != nil {
		return nil, err
	}

	var lastSync sql.NullTime
	_ = d.conn.QueryRow(`SELECT MAX(synced_at) FROM sync_log WHERE error_msg IS NULL`).Scan(&lastSync)
	if lastSync.Valid {
		s.LastSyncAt = &lastSync.Time
	}

	return &s, nil
}

// ---------------------------------------------------------------------------
// Sync log
// ---------------------------------------------------------------------------

func (d *DB) LogSync(upserted, total int, incremental bool, syncErr error) error {
	var errMsg *string
	if syncErr != nil {
		s := syncErr.Error()
		errMsg = &s
	}
	_, err := d.conn.Exec(`
		INSERT INTO sync_log (upserted, total, incremental, error_msg)
		VALUES ($1, $2, $3, $4)
	`, upserted, total, incremental, errMsg)
	return err
}

// LastSyncTime returns the timestamp of the last successful sync.
func (d *DB) LastSyncTime() (time.Time, error) {
	var t sql.NullTime
	err := d.conn.QueryRow(`
		SELECT MAX(synced_at) FROM sync_log WHERE error_msg IS NULL
	`).Scan(&t)
	if !t.Valid {
		return time.Time{}, err
	}
	return t.Time, err
}
