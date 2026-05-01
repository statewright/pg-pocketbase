//go:build !postgres

package core

// logsStatsDateExpr truncates timestamps to the hour for grouping.
const logsStatsDateExpr = "strftime('%Y-%m-%d %H:00:00', created)"
