package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

func TestESOpenIDIndex(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 2)
	f.addDoc("orders", "ord-1", `{"order_no":88121}`)
	openESIndex(t, m, "orders", targetIndex)
	press(t, m, "space", "k")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, "ord-1")
	press(t, m, "enter")
	if _, ok := m.overlay.(*esDocDetail); !ok {
		t.Fatalf("overlay = %T (status %q)", m.overlay, m.status)
	}
	if !slices.Contains(f.sent(), "GET orders/_doc/ord-1") {
		t.Errorf("sent %v", f.sent())
	}
	if !strings.Contains(screen(m), `"order_no": 88121`) {
		t.Errorf("screen:\n%s", screen(m))
	}
}

func TestESOpenIDAlias(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders-1", ordersMapping, 0)
	f.addIndex("orders-2", ordersMapping, 0)
	f.aliases = []es.Alias{{Name: "orders", Index: "orders-1"}, {Name: "orders", Index: "orders-2"}}
	f.addDoc("orders-1", "x", `{"a":1}`)
	f.addDoc("orders-2", "x", `{"a":2}`)
	tab := openESIndex(t, m, "orders", targetAlias)
	press(t, m, "space", "k")
	typeText(t, m, "x")
	press(t, m, "enter")
	if m.overlay != nil {
		t.Fatalf("overlay = %T", m.overlay)
	}
	if tab.docs.query != `{"ids":{"values":["x"]}}` || docRows(tab) != 2 {
		t.Errorf("query %q rows %d", tab.docs.query, docRows(tab))
	}
	f.addDoc("orders-1", "only", `{"a":3}`)
	press(t, m, "space", "k")
	typeText(t, m, "only")
	press(t, m, "enter")
	if d, ok := m.overlay.(*esDocDetail); !ok || d.hit().Index != "orders-1" {
		t.Errorf("one hit: overlay = %T", m.overlay)
	}
}

func TestESOpenIDFromConnection(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 0)
	f.addDoc("orders", "ord-1", `{"a":1}`)
	pressAll(t, m, "l")
	selectKafkaNode(t, m, func(n *tree.Node) bool { return n.Kind == tree.KindConn })
	press(t, m, "space", "k")
	typeText(t, m, "orders/ord-1")
	press(t, m, "enter")
	if _, ok := m.overlay.(*esDocDetail); !ok || !slices.Contains(f.sent(), "GET orders/_doc/ord-1") {
		t.Fatalf("overlay = %T, sent %v", m.overlay, f.sent())
	}
	press(t, m, "esc")
	press(t, m, "space", "k")
	typeText(t, m, "ord-1")
	press(t, m, "enter")
	if _, ok := m.overlay.(*esDocDetail); !ok || !slices.Contains(f.sent(), "POST *,-.*/_search") {
		t.Fatalf("bare id: overlay = %T, sent %v", m.overlay, f.sent())
	}
}

func TestESOpenIDNotFound(t *testing.T) {
	m, _, f := newESModel(t, config.Connection{})
	f.addIndex("orders", ordersMapping, 1)
	openESIndex(t, m, "orders", targetIndex)
	press(t, m, "space", "k")
	typeText(t, m, "ord-88121")
	press(t, m, "enter")
	if m.status != "no document with _id ord-88121 in orders" {
		t.Errorf("status = %q", m.status)
	}
}
