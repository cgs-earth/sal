package pkg

import (
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"

	"github.com/KimMachineGun/automemlimit/memlimit"
)

// The shares of the memory budget sal hands out. The Go heap and DuckDB are
// rarely busy at once in a build, since module output is streamed to disk
// while the Go side parses it and DuckDB only runs the diff afterwards, so the
// shares add up to more than the whole; DuckDB spills what does not fit in its
// share to disk rather than failing.
const (
	goMemoryShare     = 0.7
	duckDBMemoryShare = 0.5
)

// MemoryBudget is how much memory sal may use on the machine it runs on.
type MemoryBudget struct {
	Bytes uint64
	// Source says where Bytes came from: the cgroup the process runs in, which
	// is what a container's memory limit is, or the machine's physical memory.
	Source string
}

// ScratchDirName is the directory under .sal/data that a build spools what it
// commits to and that DuckDB spills to while it diffs the build against the
// table. It holds nothing once a build is done; one a killed build left is
// cleared by the next build, skipped by an upload, and removed by a wipe.
const ScratchDirName = ".scratch"

// MemoryLimitEnv overrides the memory budget sal detects, for a machine where
// the cgroup limit or the physical memory is not what sal should plan for. It
// takes a size in GOMEMLIMIT's syntax, such as 8GiB.
const MemoryLimitEnv = "SAL_MEMORY_LIMIT"

var (
	budgetOnce sync.Once
	budget     MemoryBudget
)

// Budget is SAL_MEMORY_LIMIT when it is set, else the memory limit of the cgroup sal runs in, which is what a
// container such as a Cloud Run job is killed at, and the machine's physical
// memory when there is none. It is read once per process, so nobody has to
// tell sal how much memory it has.
func Budget() MemoryBudget {
	budgetOnce.Do(func() { budget = detectBudget() })
	return budget
}

// detectBudget is Budget, worked out afresh.
func detectBudget() MemoryBudget {
	if value := os.Getenv(MemoryLimitEnv); value != "" {
		if limit, err := ParseByteSize(value); err == nil && limit > 0 {
			return MemoryBudget{Bytes: limit, Source: MemoryLimitEnv}
		}
		slog.Warn("Ignoring " + MemoryLimitEnv + "=" + value + ", which is not a size such as 8GiB")
	}
	if limit, err := memlimit.FromCgroup(); err == nil && limit > 0 {
		return MemoryBudget{Bytes: limit, Source: "cgroup limit"}
	}
	if total, err := memlimit.FromSystem(); err == nil && total > 0 {
		return MemoryBudget{Bytes: total, Source: "physical memory"}
	}
	return MemoryBudget{}
}

// ApplyGoMemoryLimit sets the Go runtime's soft memory limit to a share of
// the memory budget, so that the garbage collector works harder as the heap
// nears what the machine has rather than letting it grow to twice the live
// data first. A GOMEMLIMIT the user set wins, and with no budget known nothing
// is set. It returns the limit in force, 0 for none.
func ApplyGoMemoryLimit() int64 {
	if os.Getenv("GOMEMLIMIT") != "" {
		return debug.SetMemoryLimit(-1)
	}
	available := Budget()
	if available.Bytes == 0 {
		return 0
	}
	limit := int64(float64(available.Bytes) * goMemoryShare)
	debug.SetMemoryLimit(limit)
	return limit
}

// DuckDBMemoryLimit is the memory_limit a DuckDB database sal opens for heavy
// work is given, in bytes: a share of the memory budget, which DuckDB spills
// to disk beyond. It is 0 when no budget is known, which leaves DuckDB's own
// default in force.
func DuckDBMemoryLimit() uint64 {
	return uint64(float64(Budget().Bytes) * duckDBMemoryShare)
}

// LogMemoryBudget logs the memory budget and what sal makes of it, so a build
// that runs out of memory says what it believed it had.
func LogMemoryBudget() {
	available := Budget()
	if available.Bytes == 0 {
		slog.Warn("Could not tell how much memory this machine has; the Go and DuckDB memory limits are left at their defaults")
		return
	}
	goLimit := "GOMEMLIMIT " + os.Getenv("GOMEMLIMIT")
	if os.Getenv("GOMEMLIMIT") == "" {
		goLimit = FormatBytes(uint64(debug.SetMemoryLimit(-1)))
	}
	slog.Info("Memory budget "+FormatBytes(available.Bytes)+" ("+available.Source+")",
		"go_soft_limit", goLimit, "duckdb_memory_limit", FormatBytes(DuckDBMemoryLimit()))
}

// FormatBytes renders a byte count in binary units, like 1.5 GiB.
func FormatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// ParseByteSize reads a size the way GOMEMLIMIT does: a number with an
// optional B, KiB, MiB, GiB, or TiB suffix.
func ParseByteSize(value string) (uint64, error) {
	units := []struct {
		suffix string
		bytes  uint64
	}{{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}}
	multiplier := uint64(1)
	for _, unit := range units {
		if strings.HasSuffix(value, unit.suffix) {
			value, multiplier = strings.TrimSuffix(value, unit.suffix), unit.bytes
			break
		}
	}
	number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || number < 0 {
		return 0, fmt.Errorf("%q is not a size", value)
	}
	return uint64(number * float64(multiplier)), nil
}
