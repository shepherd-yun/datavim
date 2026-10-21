package esx

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// HumanBytes is 1234 -> "1.2 KB".
func HumanBytes(n int64) string {
	switch {
	case n < 1<<10:
		return strconv.FormatInt(n, 10) + " B"
	case n < 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KB"
	case n < 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	}
	return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
}

// Grouped is 12000 -> "12,000".
func Grouped(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func uptimeText(ms int64) string {
	m := ms / 60000
	d, h := m/(24*60), m/60%24
	m %= 60
	if d > 0 {
		return fmt.Sprintf("%dd %dh %dm", d, h, m)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

type nodeStats struct {
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
	JVM   *struct {
		Uptime int64 `json:"uptime_in_millis"`
		Mem    struct {
			Used int64 `json:"heap_used_in_bytes"`
			Max  int64 `json:"heap_max_in_bytes"`
			Pct  int   `json:"heap_used_percent"`
		} `json:"mem"`
		GC struct {
			Collectors map[string]struct {
				Count int64 `json:"collection_count"`
			} `json:"collectors"`
		} `json:"gc"`
	} `json:"jvm"`
	OS *struct {
		CPU struct {
			Percent *int                `json:"percent"`
			Load    map[string]*float64 `json:"load_average"`
		} `json:"cpu"`
		Mem *struct {
			Total int64 `json:"total_in_bytes"`
			Used  int64 `json:"used_in_bytes"`
		} `json:"mem"`
	} `json:"os"`
	FS *struct {
		Total *struct {
			Total int64 `json:"total_in_bytes"`
			Avail int64 `json:"available_in_bytes"`
		} `json:"total"`
	} `json:"fs"`
	Process *struct {
		FDs *int64 `json:"open_file_descriptors"`
	} `json:"process"`
	Indices *struct {
		Docs *struct {
			Count int64 `json:"count"`
		} `json:"docs"`
		Store *struct {
			Size int64 `json:"size_in_bytes"`
		} `json:"store"`
		Search *struct {
			Total int64 `json:"query_total"`
		} `json:"search"`
		Indexing *struct {
			Total int64 `json:"index_total"`
		} `json:"indexing"`
	} `json:"indices"`
	ThreadPool map[string]struct {
		Rejected int64 `json:"rejected"`
	} `json:"thread_pool"`
}

// NodeSummary is the main figures of one node's _nodes/stats, in a fixed
// order; a figure the stats lack is left out.
func NodeSummary(stats json.RawMessage) [][2]string {
	var s nodeStats
	if err := json.Unmarshal(stats, &s); err != nil {
		return [][2]string{{"error", err.Error()}}
	}
	var out [][2]string
	add := func(k, v string) { out = append(out, [2]string{k, v}) }
	if s.Name != "" {
		add("name", s.Name)
	}
	if len(s.Roles) > 0 {
		add("roles", strings.Join(s.Roles, ", "))
	}
	if j := s.JVM; j != nil {
		add("heap", fmt.Sprintf("%s / %s (%d%%)", HumanBytes(j.Mem.Used), HumanBytes(j.Mem.Max), j.Mem.Pct))
		young, yok := j.GC.Collectors["young"]
		old, ook := j.GC.Collectors["old"]
		if yok && ook {
			add("gc young / old", fmt.Sprintf("%d / %d", young.Count, old.Count))
		}
	}
	if o := s.OS; o != nil {
		if o.CPU.Percent != nil {
			add("cpu", strconv.Itoa(*o.CPU.Percent)+"%")
		}
		l1, l5, l15 := o.CPU.Load["1m"], o.CPU.Load["5m"], o.CPU.Load["15m"]
		if l1 != nil && l5 != nil && l15 != nil {
			add("load 1m / 5m / 15m", fmt.Sprintf("%.2f / %.2f / %.2f", *l1, *l5, *l15))
		}
		if o.Mem != nil {
			add("ram", HumanBytes(o.Mem.Used)+" / "+HumanBytes(o.Mem.Total))
		}
	}
	if s.FS != nil && s.FS.Total != nil {
		add("disk total / available", HumanBytes(s.FS.Total.Total)+" / "+HumanBytes(s.FS.Total.Avail))
	}
	if s.Process != nil && s.Process.FDs != nil {
		add("open files", Grouped(*s.Process.FDs))
	}
	if i := s.Indices; i != nil {
		if i.Docs != nil {
			add("docs", Grouped(i.Docs.Count))
		}
		if i.Store != nil {
			add("store", HumanBytes(i.Store.Size))
		}
		if i.Search != nil {
			add("queries", Grouped(i.Search.Total))
		}
		if i.Indexing != nil {
			add("indexing", Grouped(i.Indexing.Total))
		}
	}
	search, sok := s.ThreadPool["search"]
	write, wok := s.ThreadPool["write"]
	if sok && wok {
		add("rejected search / write", fmt.Sprintf("%d / %d", search.Rejected, write.Rejected))
	}
	if s.JVM != nil && s.JVM.Uptime > 0 {
		add("uptime", uptimeText(s.JVM.Uptime))
	}
	return out
}

// ExplainSummary is an allocation explanation as lines: the shard, why it
// is (not) allocated, then each node's NO / THROTTLE deciders.
func ExplainSummary(explain json.RawMessage) []string {
	var e struct {
		Index      string `json:"index"`
		Shard      int    `json:"shard"`
		Primary    bool   `json:"primary"`
		State      string `json:"current_state"`
		Unassigned *struct {
			Reason string `json:"reason"`
			Last   string `json:"last_allocation_status"`
			Detail string `json:"details"`
		} `json:"unassigned_info"`
		Allocate string `json:"allocate_explanation"`
		Move     string `json:"move_explanation"`
		Nodes    []struct {
			Name     string `json:"node_name"`
			Deciders []struct {
				Decider     string `json:"decider"`
				Decision    string `json:"decision"`
				Explanation string `json:"explanation"`
			} `json:"deciders"`
		} `json:"node_allocation_decisions"`
	}
	if err := json.Unmarshal(explain, &e); err != nil {
		return []string{"cannot read the explanation: " + err.Error()}
	}
	kind := "replica"
	if e.Primary {
		kind = "primary"
	}
	out := []string{fmt.Sprintf("%s[%d] %s %s", e.Index, e.Shard, kind, e.State)}
	for _, s := range []string{e.Allocate, e.Move} {
		if s != "" {
			out = append(out, s)
		}
	}
	if u := e.Unassigned; u != nil {
		line := "reason: " + u.Reason
		if u.Last != "" {
			line += " · last allocation: " + u.Last
		}
		out = append(out, line)
		if u.Detail != "" {
			out = append(out, u.Detail)
		}
	}
	for _, n := range e.Nodes {
		for _, d := range n.Deciders {
			if d.Decision == "NO" || d.Decision == "THROTTLE" {
				out = append(out, n.Name+": "+d.Decider+": "+d.Explanation)
			}
		}
	}
	return out
}

var byteUnits = map[string]float64{
	"b": 1, "kb": 1 << 10, "mb": 1 << 20, "gb": 1 << 30, "tb": 1 << 40, "pb": 1 << 50,
}

// Watermark is a disk watermark setting as a used percentage: "85%" and
// "0.85" are 85; a byte value ("50gb") is free space, so of total it is
// (total-free)/total.
func Watermark(value string, total int64) (usedPct float64, ok bool) {
	v := strings.ToLower(strings.TrimSpace(value))
	if p, found := strings.CutSuffix(v, "%"); found {
		f, err := strconv.ParseFloat(p, 64)
		return f, err == nil
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		if f <= 1 {
			return f * 100, true
		}
		return 0, false
	}
	if total <= 0 {
		return 0, false
	}
	for _, u := range []string{"kb", "mb", "gb", "tb", "pb", "b"} {
		if n, found := strings.CutSuffix(v, u); found {
			f, err := strconv.ParseFloat(n, 64)
			if err != nil {
				return 0, false
			}
			free := f * byteUnits[u]
			return (float64(total) - free) / float64(total) * 100, true
		}
	}
	return 0, false
}
