package esx

import (
	"reflect"
	"testing"
)

func TestHitsTable(t *testing.T) {
	resp := `{"took":3,"hits":{"total":{"value":2,"relation":"eq"},"hits":[
	  {"_index":"orders","_id":"1","_score":1.0,"_source":{"no":1,"user":{"name":"a"},"tags":["x"]}},
	  {"_index":"orders","_id":"2","_score":1.0,"_source":{"no":2,"extra":null}}
	]}}`
	got, err := HitsTable([]byte(resp))
	if err != nil {
		t.Fatal(err)
	}
	want := Table{
		Columns: []string{"_index", "_id", "no", "user.name", "tags", "extra"},
		Rows: [][]string{
			{"orders", "1", "1", "a", `["x"]`, ""},
			{"orders", "2", "2", "", "", "null"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if _, err := HitsTable([]byte(`{"acknowledged":true}`)); err == nil {
		t.Error("no error for a non-search response")
	}
}

func TestCatTable(t *testing.T) {
	got, err := CatTable([]byte(`[{"health":"green","index":"a","docs.count":"10"},{"health":"yellow","index":"b","docs.count":"3","x":"1"}]`))
	if err != nil {
		t.Fatal(err)
	}
	want := Table{
		Columns: []string{"health", "index", "docs.count", "x"},
		Rows:    [][]string{{"green", "a", "10", ""}, {"yellow", "b", "3", "1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	if _, err := CatTable([]byte(`{"a":1}`)); err == nil {
		t.Error("no error for an object")
	}
}

func TestAggTable(t *testing.T) {
	resp := `{"hits":{"hits":[]},"aggregations":{
	  "by_status":{"buckets":[
	    {"key":"paid","doc_count":10,"by_city":{"buckets":[
	      {"key":"sh","doc_count":6,"avg_amount":{"value":12.5}},
	      {"key":"bj","doc_count":4,"avg_amount":{"value":null}}
	    ]}},
	    {"key":"new","doc_count":2,"by_city":{"buckets":[
	      {"key":"sh","doc_count":2,"avg_amount":{"value":3}}
	    ]}}
	  ]},
	  "other":{"value":1}
	}}`
	got, err := AggTable([]byte(resp))
	if err != nil {
		t.Fatal(err)
	}
	want := Table{
		Columns: []string{"by_status", "by_city", "doc_count", "avg_amount"},
		Rows: [][]string{
			{"paid", "sh", "6", "12.5"},
			{"paid", "bj", "4", "null"},
			{"new", "sh", "2", "3"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	got, err = AggTable([]byte(`{"aggregations":{"max_price":{"value":9}}}`))
	if err != nil || !reflect.DeepEqual(got, Table{Columns: []string{"max_price"}, Rows: [][]string{{"9"}}}) {
		t.Errorf("metric: %+v %v", got, err)
	}
	if _, err := AggTable([]byte(`{"hits":{}}`)); err == nil {
		t.Error("no error without aggregations")
	}
}
