package ui

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/shepherd-yun/datavim/internal/rdb"
)

const (
	// statusInterval is how often the status section samples INFO.
	statusInterval = 2 * time.Second
	// statusSamples is how many samples the sparklines keep (5 minutes).
	statusSamples = 150
)

// statusSample is one INFO's numbers for the sparklines; NaN when the
// server did not report the field.
type statusSample struct{ ops, mem, inKbps, outKbps float64 }

func sampleOf(i rdb.Info) statusSample {
	f := func(field string) float64 {
		if v, ok := i.Float(field); ok {
			return v
		}
		return math.NaN()
	}
	return statusSample{
		ops: f("instantaneous_ops_per_sec"), mem: f("used_memory"),
		inKbps: f("instantaneous_input_kbps"), outKbps: f("instantaneous_output_kbps"),
	}
}

// fact is one line of the status section.
type fact struct{ label, value string }

const missing = "—"

// statusFacts are the status section's lines; a field the server did not
// report is "—".
func statusFacts(i rdb.Info) []fact {
	get := func(field string) string {
		if v := i[field]; v != "" {
			return v
		}
		return missing
	}
	return []fact{
		{"version", get("redis_version")},
		{"role", roleFact(i)},
		{"uptime", intFact(i, "uptime_in_seconds", uptime)},
		{"clients", clientsFact(i)},
		{"keys", keysFact(i)},
		{"memory", memoryFact(i)},
		{"hit rate", hitRate(i)},
		{"evicted", intFact(i, "evicted_keys", groupDigits)},
		{"expired", intFact(i, "expired_keys", groupDigits)},
	}
}

func intFact(i rdb.Info, field string, format func(int64) string) string {
	if n, ok := i.Int(field); ok {
		return format(n)
	}
	return missing
}

func roleFact(i rdb.Info) string {
	role := i["role"]
	switch role {
	case "":
		return missing
	case "master":
		if n, ok := i.Int("connected_slaves"); ok {
			return role + " · " + plural64(n, "replica")
		}
	default:
		if host := i["master_host"]; host != "" {
			role += " of " + host + ":" + i["master_port"]
		}
		if link := i["master_link_status"]; link != "" {
			role += " · link " + link
		}
	}
	return role
}

func clientsFact(i rdb.Info) string {
	n, ok := i.Int("connected_clients")
	if !ok {
		return missing
	}
	s := groupDigits(n)
	if b, ok := i.Int("blocked_clients"); ok && b > 0 {
		s += " · " + groupDigits(b) + " blocked"
	}
	return s
}

// keysFact sums the keyspace lines; a server that sent no keyspace lines
// but did answer has 0 keys.
func keysFact(i rdb.Info) string {
	n := i.Keys()
	if _, answered := i["redis_version"]; n == 0 && !answered {
		return missing
	}
	return groupDigits(n)
}

func memoryFact(i rdb.Info) string {
	used := i["used_memory_human"]
	if used == "" {
		return missing
	}
	peak := i["used_memory_peak_human"]
	if peak == "" {
		peak = missing
	}
	limit := missing
	if n, ok := i.Int("maxmemory"); ok && n == 0 {
		limit = "no limit"
	} else if h := i["maxmemory_human"]; h != "" {
		limit = h
	}
	s := used + " · peak " + peak + " · max " + limit
	if p := i["maxmemory_policy"]; p != "" {
		s += " (" + p + ")"
	}
	return s
}

func hitRate(i rdb.Info) string {
	hits, ok1 := i.Int("keyspace_hits")
	misses, ok2 := i.Int("keyspace_misses")
	if !ok1 || !ok2 || hits+misses == 0 {
		return missing
	}
	return strconv.FormatFloat(float64(hits)*100/float64(hits+misses), 'f', 1, 64) + "%"
}

// uptime is 273600 -> "3d 4h", 15120 -> "4h 12m", 725 -> "12m 5s".
func uptime(s int64) string {
	d, h, m := s/86400, s%86400/3600, s%3600/60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s%60)
	}
	return fmt.Sprintf("%ds", s)
}
