//go:build postgres

package migrations

func auxInitLogsSQL() string {
	return `
		CREATE TABLE IF NOT EXISTS {{_logs}} (
			[[id]]      TEXT PRIMARY KEY DEFAULT ('r'||lower(hex(randomblob(7)))) NOT NULL,
			[[level]]   INTEGER DEFAULT 0 NOT NULL,
			[[message]] TEXT DEFAULT '' NOT NULL,
			[[data]]    JSON DEFAULT '{}' NOT NULL,
			[[created]] TEXT DEFAULT (to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.MSZ')) NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_logs_level on {{_logs}} ([[level]]);
		CREATE INDEX IF NOT EXISTS idx_logs_message on {{_logs}} ([[message]]);
		CREATE INDEX IF NOT EXISTS idx_logs_created_hour on {{_logs}} (left([[created]], 13));
	`
}
