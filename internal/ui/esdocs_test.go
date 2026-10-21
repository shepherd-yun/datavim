package ui

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

func docRows(tab *esIndexTab) int {
	if rs := tab.docs.grid.Result(); rs != nil {
		return len(rs.Rows)
	}
	return 0
}

func docCols(tab *esIndexTab) []string {
	var out []string
	if rs := tab.docs.grid.Result(); rs != nil {
		for _, c := range rs.Columns {
			out = append(out, c.Name)
		}
	}
	return out
}

func TestESDocsLoad(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 250)
	tab := openESIndex(t, m, "orders", targetIndex)
	if n := docRows(tab); n != 100 {
		t.Fatalf("first page = %d rows", n)
	}
	if c := docCols(tab); c[0] != "_id" || slices.Contains(c, "_index") {
		t.Errorf("columns = %v", c)
	}
	if !strings.Contains(screen(m), "250 hits") {
		t.Errorf("screen:\n%s", screen(m))
	}
	press(t, m, "G", "j")
	if n := docRows(tab); n != 200 {
		t.Fatalf("after the last row = %d rows", n)
	}
	press(t, m, "}")
	if n := docRows(tab); n != 250 || !strings.Contains(tab.status, "end of results") {
		t.Fatalf("rows %d status %q", n, tab.status)
	}
	press(t, m, "}")
	if n := docRows(tab); n != 250 || !strings.Contains(tab.status, "end of results") {
		t.Fatalf("again: rows %d status %q", n, tab.status)
	}
}

func TestESDocsPageSize(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{Params: map[string]string{"page_size": "20"}})
	f.addIndex("orders", ordersMapping, 50)
	tab := openESIndex(t, m, "orders", targetIndex)
	if n := docRows(tab); n != 20 {
		t.Fatalf("rows = %d", n)
	}
}

func TestESDocsMemoryCap(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 400)
	tab := openESIndex(t, m, "orders", targetIndex)
	tab.docs.limit = 250
	press(t, m, "}", "}")
	if n := docRows(tab); n != 250 || !strings.Contains(tab.status, "dropped the first 50 rows (keeping 250)") {
		t.Fatalf("rows %d status %q", n, tab.status)
	}
	if tab.docs.hits[0].ID != "d50" {
		t.Errorf("first kept = %s", tab.docs.hits[0].ID)
	}
}

func TestESDocsDefaultSort(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 3)
	openESIndex(t, m, "orders", targetIndex)
	if b := f.lastBody("POST _search"); !strings.Contains(b, `{"@timestamp":{"order":"desc"}}`) {
		t.Errorf("body = %s", b)
	}
	m2, _, f2 := newESModel(t, config.Connection{})
	f2.addIndex("plain", `{"properties":{"a":{"type":"keyword"}}}`, 3)
	openESIndex(t, m2, "plain", targetIndex)
	if b := f2.lastBody("POST _search"); !strings.Contains(b, `"sort":[{"_shard_doc":"asc"}]`) {
		t.Errorf("no timestamp: body = %s", b)
	}
}

func TestESDocsSortText(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", `{"properties":{"title":{"type":"text","fields":{"keyword":{"type":"keyword"}}},"body":{"type":"text"},"no":{"type":"long"}}}`, 3)
	tab := openESIndex(t, m, "orders", targetIndex)
	cols := docCols(tab)
	at := slices.Index(cols, "title")
	if at < 0 {
		t.Fatalf("columns = %v", cols)
	}
	for range at {
		press(t, m, "l")
	}
	press(t, m, "s")
	if b := f.lastBody("POST _search"); !strings.Contains(b, `{"title.keyword":{"order":"asc"}}`) {
		t.Errorf("body = %s", b)
	}
	press(t, m, "s")
	if b := f.lastBody("POST _search"); !strings.Contains(b, `{"title.keyword":{"order":"desc"}}`) {
		t.Errorf("desc body = %s", b)
	}
	// a text field without a keyword sub-field is not a default column;
	// sorting on it is refused all the same
	if err := tab.docs.toggleSort("body"); err == nil || err.Error() != "cannot sort on text field; use body.keyword" {
		t.Errorf("err = %v", err)
	}
}

func TestESDocsExpired(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 250)
	tab := openESIndex(t, m, "orders", targetIndex)
	f.mu.Lock()
	clear(f.cursors) // the PIT expired
	f.mu.Unlock()
	press(t, m, "}")
	if !strings.Contains(tab.status, "the result cursor expired; reloading from the top") {
		t.Errorf("status = %q", tab.status)
	}
	if n := docRows(tab); n != 100 || tab.docs.hits[0].ID != "d0" {
		t.Errorf("rows %d", n)
	}
	press(t, m, "}")
	if n := docRows(tab); n != 200 {
		t.Errorf("after reload: rows %d", n)
	}
}

func TestESDocsEmpty(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("fresh", "", 0)
	tab := openESIndex(t, m, "fresh", targetIndex)
	if s := screen(m); !strings.Contains(s, "no documents") {
		t.Errorf("screen:\n%s", s)
	}
	if c := docCols(tab); !slices.Equal(c, []string{"_id"}) && c != nil {
		t.Errorf("columns = %v", c)
	}
}

func TestESDocsCloseReleases(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 250)
	openESIndex(t, m, "orders", targetIndex)
	if f.openCursors() != 1 {
		t.Fatalf("open cursors = %d", f.openCursors())
	}
	drain(t, m, m.closeTab(m.active))
	if f.openCursors() != 0 || !slices.Contains(f.sent(), "DELETE _pit") {
		t.Errorf("open %d, sent %v", f.openCursors(), f.sent())
	}
}

func TestESDocsQuitReleases(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 250)
	openESIndex(t, m, "orders", targetIndex)
	m.quit()()
	if f.openCursors() != 0 {
		t.Errorf("open %d", f.openCursors())
	}
}

func TestESDocDetail(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 0)
	f.addDoc("orders", "ord-1", `{"order_no":88121,"user":{"name":"张三"},"tags":["a","b"]}`).seq = 1022
	f.addDoc("orders", "ord-2", `{"order_no":2}`)
	tab := openESIndex(t, m, "orders", targetIndex)
	press(t, m, "K")
	d, ok := m.overlay.(*esDocDetail)
	if !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	v := screen(m)
	for _, want := range []string{"orders · ord-1", "_seq_no 1022", "_primary_term 1", `"order_no": 88121`, `"name": "张三"`} {
		if !strings.Contains(v, want) {
			t.Errorf("detail lacks %q:\n%s", want, v)
		}
	}
	press(t, m, "j", "j", "z", "c") // fold "user"
	if v := screen(m); strings.Contains(v, `"name": "张三"`) || !strings.Contains(v, `"user": {…}`) {
		t.Errorf("folded:\n%s", v)
	}
	press(t, m, "z", "o")
	if v := screen(m); !strings.Contains(v, `"name": "张三"`) {
		t.Errorf("unfolded:\n%s", v)
	}
	press(t, m, "n")
	if d.hit().ID != "ord-2" {
		t.Errorf("next = %s", d.hit().ID)
	}
	press(t, m, "esc")
	tab.docs.hits[0].Truncated, tab.docs.hits[0].SourceSize = true, 3<<20
	press(t, m, "k", "K")
	if v := screen(m); !strings.Contains(v, "showing first 1 MB of 3.0 MB") {
		t.Errorf("truncated:\n%s", v)
	}
}

func TestESDocsYank(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{Host: "es1", User: "elastic"})
	f.addIndex("orders", ordersMapping, 0)
	f.addDoc("orders", "ord-1", `{"order_no":88121}`)
	clip := captureClipboard(t)
	openESIndex(t, m, "orders", targetIndex)
	press(t, m, "y", "k")
	if *clip != "ord-1" {
		t.Errorf("yk = %q", *clip)
	}
	press(t, m, "y", "s")
	if *clip != "{\n  \"order_no\": 88121\n}" {
		t.Errorf("ys = %q", *clip)
	}
	press(t, m, "y", "r")
	if *clip != "GET orders/_doc/ord-1\n\nPUT orders/_doc/ord-1\n{\n  \"order_no\": 88121\n}\n" {
		t.Errorf("yr = %q", *clip)
	}
	press(t, m, "y", "u")
	if *clip != "curl -u 'elastic' -X GET 'http://es1:9200/orders/_doc/ord-1'" {
		t.Errorf("yu = %q", *clip)
	}
	m2, _, f2 := newESModel(t, config.Connection{Host: "https://es1:9243", Params: map[string]string{"auth": "apikey", "tls_skip_verify": "true"}})
	f2.addIndex("orders", ordersMapping, 1)
	openESIndex(t, m2, "orders", targetIndex)
	press(t, m2, "y", "u")
	if *clip != "curl -k -H 'Authorization: ApiKey <api-key>' -X GET 'https://es1:9243/orders/_doc/d0'" {
		t.Errorf("yu api key = %q", *clip)
	}
}

func TestESDocsMultiIndex(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders-1", ordersMapping, 2)
	f.addIndex("orders-2", ordersMapping, 2)
	f.aliases = []es.Alias{{Name: "orders", Index: "orders-1"}, {Name: "orders", Index: "orders-2"}}
	tab := openESIndex(t, m, "orders", targetAlias)
	if c := docCols(tab); len(c) < 2 || c[1] != "_index" {
		t.Errorf("columns = %v", c)
	}
	if rs := tab.docs.grid.Result(); rs.Rows[2][1].Text != "orders-2" {
		t.Errorf("rows = %+v", rs.Rows)
	}
}

func TestESDocsExportNDJSON(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 2)
	tab := openESIndex(t, m, "orders", targetIndex)
	src := tab.exportSource()
	if src.jsonl == nil || len(src.jsonl.Rows) != 2 {
		t.Fatalf("export = %+v", src)
	}
	var doc struct {
		Index  string          `json:"_index"`
		ID     string          `json:"_id"`
		Source json.RawMessage `json:"_source"`
	}
	if err := json.Unmarshal([]byte(src.jsonl.Rows[0][0].Text), &doc); err != nil || doc.Index != "orders" || doc.ID != "d0" || string(doc.Source) != `{"n":0}` {
		t.Errorf("row = %s", src.jsonl.Rows[0][0].Text)
	}
	_ = overlay.CopyMsg{}
}
