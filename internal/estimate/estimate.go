// Package estimate turns (operation kind x row count) into a lock-duration range.
// The throughput constants are rough defaults for commodity hardware; real
// duration depends on load, hardware and concurrent queries, so results are
// always ranges, never promises.
package estimate

import (
	"fmt"
	"math"
	"time"
)

// Kind describes how an operation scales with table size.
type Kind struct {
	Name string
	// Rows processed per second, slowest and fastest plausible.
	SlowRPS, FastRPS float64
}

var (
	// Calibrated against PG 16 in Docker on a laptop (3M rows): rewrites ~46-56k rows/s,
	// text index/unique builds ~40-64k, int index ~160k, scans ~380-440k. The slow end of
	// each range is that measurement; the fast end allows for server-class NVMe hardware.

	// Rewrite: full table rewrite (ALTER TYPE, volatile default).
	Rewrite = &Kind{"rewrite", 40_000, 300_000}
	// IndexBuild: building a b-tree index (CREATE INDEX, ADD UNIQUE/PK).
	IndexBuild = &Kind{"index-build", 40_000, 400_000}
	// Scan: validating a constraint by scanning the table (NOT NULL, FOREIGN KEY).
	Scan = &Kind{"scan", 300_000, 2_000_000}

	// MySQL 8.0.46 (InnoDB) in Docker on a laptop, 3M rows: table copies 12.7k (ADD FOREIGN KEY,
	// which also validates) to 30.6k rows/s (MODIFY int->bigint, ADD CHECK); in-place rebuilds
	// 38-40k; secondary index builds 62k-100k. The slow end is that measurement; the fast end allows
	// for server-class hardware.
	MySQLCopy    = &Kind{"mysql-copy", 12_000, 120_000}    // ALGORITHM=COPY: writes are blocked for the duration
	MySQLRebuild = &Kind{"mysql-rebuild", 35_000, 280_000} // INPLACE table rebuild: writes continue
	MySQLIndex   = &Kind{"mysql-index", 55_000, 450_000}   // INPLACE secondary index build: writes continue
)

// Range is an approximate lock duration.
type Range struct{ Min, Max time.Duration }

// For estimates the duration of kind k over rows rows.
func For(k *Kind, rows int64) Range {
	r := float64(rows)
	return Range{
		Min: secs(r / k.FastRPS),
		Max: secs(r / k.SlowRPS),
	}
}

func secs(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// String renders e.g. "23-93s" or "1-3 min".
func (r Range) String() string {
	lo, hi := round(r.Min), round(r.Max)
	if hi < 180*time.Second {
		if lo == hi {
			return fmt.Sprintf("~%ds", int(lo.Seconds()))
		}
		return fmt.Sprintf("%d-%ds", int(lo.Seconds()), int(hi.Seconds()))
	}
	return fmt.Sprintf("%d-%d min", int(math.Round(lo.Minutes())), int(math.Ceil(hi.Minutes())))
}

func round(d time.Duration) time.Duration {
	if d < time.Second {
		return time.Second
	}
	return d.Round(time.Second)
}

// HumanRows renders 14_200_000 as "14.2M".
func HumanRows(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}
