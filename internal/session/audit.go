package session

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
)

// auditDetailMax bounds an audit entry's Detail, in runes.
const auditDetailMax = 2000

// record fills e's Time, Duration (since start) and Err and hands it to
// m.Audit; without an Audit it does nothing.
func (m *Manager) record(e history.AuditEntry, start time.Time, err error) {
	if m.Audit == nil {
		return
	}
	e.Time, e.Duration = start, time.Since(start)
	if err != nil {
		e.Err = err.Error()
	}
	e.Detail = clipDetail(e.Detail)
	m.Audit(e)
}

// clipDetail cuts s to auditDetailMax runes, ending in "…".
func clipDetail(s string) string {
	if len(s) <= auditDetailMax {
		return s
	}
	r := []rune(s)
	if len(r) <= auditDetailMax {
		return s
	}
	return string(r[:auditDetailMax-1]) + "…"
}

// audit records a Kafka write of g's connection.
func (g *guardedKafka) audit(op kmsgx.Op, object, detail string, start time.Time, err error) {
	g.m.record(history.AuditEntry{Conn: g.name, Kind: history.AuditKafka, Op: op.String(), Object: object, Detail: detail}, start, err)
}

// maxAuditParts is how many partitions or configs a Kafka entry lists.
const maxAuditParts = 20

func joinParts(parts []string, sep string) string {
	if len(parts) > maxAuditParts {
		return strings.Join(parts[:maxAuditParts], sep) + fmt.Sprintf("%s… +%d", sep, len(parts)-maxAuditParts)
	}
	return strings.Join(parts, sep)
}

func resetDetail(commits []kafka.OffsetCommit) string {
	parts := make([]string, len(commits))
	for i, c := range commits {
		parts[i] = fmt.Sprintf("%s p%d %d → %d", c.Topic, c.Partition, c.Expect, c.To)
	}
	return joinParts(parts, ", ")
}

func configsDetail(changes []kafka.ConfigChange) string {
	parts := make([]string, len(changes))
	for i, c := range changes {
		if c.Delete {
			parts[i] = c.Name + " → default"
		} else {
			parts[i] = fmt.Sprintf("%s %s → %s", c.Name, c.Old, c.Value)
		}
	}
	return joinParts(parts, "; ")
}

func deleteRecordsDetail(before map[int32]int64) string {
	ps := make([]int32, 0, len(before))
	for p := range before {
		ps = append(ps, p)
	}
	slices.Sort(ps)
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = fmt.Sprintf("p%d before %d", p, before[p])
	}
	return joinParts(parts, ", ")
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}
