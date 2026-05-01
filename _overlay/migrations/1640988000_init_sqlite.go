//go:build !postgres

package migrations

func initParamsSQL() string {
	return `
		CREATE TABLE {{_params}} (
			[[id]]      TEXT PRIMARY KEY DEFAULT ('r'||lower(hex(randomblob(7)))) NOT NULL,
			[[value]]   JSON DEFAULT NULL,
			[[created]] TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%fZ')) NOT NULL,
			[[updated]] TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%fZ')) NOT NULL
		);
	`
}

func initCollectionsSQL() string {
	return `
		CREATE TABLE {{_collections}} (
			[[id]]         TEXT PRIMARY KEY DEFAULT ('r'||lower(hex(randomblob(7)))) NOT NULL,
			[[system]]     BOOLEAN DEFAULT FALSE NOT NULL,
			[[type]]       TEXT DEFAULT "base" NOT NULL,
			[[name]]       TEXT UNIQUE NOT NULL,
			[[fields]]     JSON DEFAULT "[]" NOT NULL,
			[[indexes]]    JSON DEFAULT "[]" NOT NULL,
			[[listRule]]   TEXT DEFAULT NULL,
			[[viewRule]]   TEXT DEFAULT NULL,
			[[createRule]] TEXT DEFAULT NULL,
			[[updateRule]] TEXT DEFAULT NULL,
			[[deleteRule]] TEXT DEFAULT NULL,
			[[options]]    JSON DEFAULT "{}" NOT NULL,
			[[created]]    TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%fZ')) NOT NULL,
			[[updated]]    TEXT DEFAULT (strftime('%Y-%m-%d %H:%M:%fZ')) NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx__collections_type on {{_collections}} ([[type]]);
	`
}
