//go:build postgres

package core

// logsStatsDateExpr truncates timestamps to the hour for grouping.
// Uses left() to extract "YYYY-MM-DD HH" prefix and appends ":00:00".
const logsStatsDateExpr = "left(created, 13) || ':00:00'"
