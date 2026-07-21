// Copyright 2026 Allen Lew and contributors. Licensed under Apache-2.0. See LICENSE.
//
// Date helpers shared by Forkable novel commands. Hand-authored; preserved
// across generate --force.

package cli

import (
	"strconv"
	"strings"
	"time"
)

// sinceCutoffISO parses a loose --since duration ("90d", "12w", "6mo", "1y",
// "24h", or a bare integer meaning days) and returns the cutoff date as a
// "YYYY-MM-DD" string. An empty or unparseable value returns "" (no bound).
func sinceCutoffISO(since string) string {
	d := parseLooseSince(since)
	if d == 0 {
		return ""
	}
	return time.Now().Add(-d).Format("2006-01-02")
}

// parseLooseSince converts a loose duration string to a time.Duration.
// Supports d (days), w (weeks), mo (months≈30d), y (years≈365d), plus the
// standard time.ParseDuration units (h, m, s). Bare integers are days.
// Returns 0 on empty/invalid input.
func parseLooseSince(since string) time.Duration {
	s := strings.TrimSpace(strings.ToLower(since))
	if s == "" {
		return 0
	}
	day := 24 * time.Hour
	switch {
	case strings.HasSuffix(s, "mo"):
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "mo")); err == nil {
			return time.Duration(n) * 30 * day
		}
	case strings.HasSuffix(s, "y"):
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "y")); err == nil {
			return time.Duration(n) * 365 * day
		}
	case strings.HasSuffix(s, "w"):
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "w")); err == nil {
			return time.Duration(n) * 7 * day
		}
	case strings.HasSuffix(s, "d"):
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil {
			return time.Duration(n) * day
		}
	}
	// Bare integer => days.
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * day
	}
	// Fall back to standard durations (24h, 90m).
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return 0
}

// dateOnOrAfter reports whether an ISO-ish date string (any prefix of
// "YYYY-MM-DD...") is on or after the cutoff (also "YYYY-MM-DD"). An empty
// cutoff always matches. An unparseable date matches (fail-open: better to
// include an ambiguous row than silently drop it).
func dateOnOrAfter(dateStr, cutoff string) bool {
	if cutoff == "" {
		return true
	}
	if len(dateStr) < 10 {
		return true
	}
	return dateStr[:10] >= cutoff
}
