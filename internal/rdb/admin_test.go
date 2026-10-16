package rdb

import (
	"errors"
	"testing"
)

func TestParseInfo(t *testing.T) {
	i := ParseInfo("# Server\r\nredis_version:7.2.4\r\nmem_fragmentation_ratio:1.25\r\n\r\n# Keyspace\r\ndb0:keys=3,expires=0,avg_ttl=0\r\ndb2:keys=5,expires=1,avg_ttl=9\r\n")
	if i["redis_version"] != "7.2.4" {
		t.Fatalf("redis_version = %q", i["redis_version"])
	}
	if i["db0"] != "keys=3,expires=0,avg_ttl=0" {
		t.Fatalf("db0 = %q", i["db0"])
	}
	if n := i.Keys(); n != 8 {
		t.Fatalf("Keys() = %d", n)
	}
	if _, ok := i.Int("redis_version"); ok {
		t.Fatal("Int(redis_version) ok")
	}
	if _, ok := i.Int("missing"); ok {
		t.Fatal("Int(missing) ok")
	}
	if f, ok := i.Float("mem_fragmentation_ratio"); !ok || f != 1.25 {
		t.Fatalf("Float = %v %v", f, ok)
	}
	if _, ok := i["# Server"]; ok {
		t.Fatal("section header kept")
	}
	if n, ok := ParseInfo("connected_clients:4\n").Int("connected_clients"); !ok || n != 4 {
		t.Fatalf("Int = %d %v", n, ok)
	}
}

func TestParseClientList(t *testing.T) {
	got := parseClientList("id=7 addr=10.0.0.1:5000 name= age=30 idle=2 db=0 cmd=client|list\nid=8 addr=10.0.0.2:5001 name=probe age=3 idle=0 db=1 cmd=get\n")
	if len(got) != 2 {
		t.Fatalf("got %d clients", len(got))
	}
	if got[0]["id"] != "7" || got[0]["addr"] != "10.0.0.1:5000" || got[0]["cmd"] != "client|list" {
		t.Fatalf("client 0 = %v", got[0])
	}
	if v, ok := got[0]["name"]; !ok || v != "" {
		t.Fatalf("name = %q %v", v, ok)
	}
	if got[1]["name"] != "probe" || got[1]["db"] != "1" {
		t.Fatalf("client 1 = %v", got[1])
	}
}

func TestIsNoPerm(t *testing.T) {
	if !IsNoPerm(replyErr("NOPERM this user has no permissions to run the 'slowlog' command")) {
		t.Fatal("NOPERM not recognized")
	}
	if IsNoPerm(replyErr("ERR unknown command 'slowlog'")) {
		t.Fatal("ERR is not NOPERM")
	}
	if IsNoPerm(errors.New("NOPERM but not a reply")) {
		t.Fatal("plain error is not NOPERM")
	}
	if IsNoPerm(nil) {
		t.Fatal("nil is not NOPERM")
	}
}
