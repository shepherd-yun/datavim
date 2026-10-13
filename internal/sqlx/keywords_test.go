package sqlx

import (
	"slices"
	"sort"
	"testing"
)

func TestKeywords(t *testing.T) {
	my, pg := Keywords(MySQL), Keywords(Postgres)
	for _, kws := range [][]string{my, pg} {
		if !sort.StringsAreSorted(kws) || len(slices.Compact(slices.Clone(kws))) != len(kws) {
			t.Fatal("sorted and unique")
		}
		if !slices.Contains(kws, "SELECT") || !slices.Contains(kws, "WHERE") {
			t.Fatal("common keywords")
		}
	}
	if !slices.Contains(my, "SHOW") || slices.Contains(my, "RETURNING") {
		t.Error("MySQL keywords")
	}
	if !slices.Contains(pg, "RETURNING") || slices.Contains(pg, "STRAIGHT_JOIN") {
		t.Error("PostgreSQL keywords")
	}
}

func TestQuoteIfNeeded(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    Dialect
		want string
	}{
		{"users", MySQL, "users"},
		{"users", Postgres, "users"},
		{"user_2", Postgres, "user_2"},
		{"order", MySQL, "`order`"},
		{"order", Postgres, `"order"`},
		{"MixedCase", MySQL, "MixedCase"},
		{"MixedCase", Postgres, `"MixedCase"`},
		{"2fa", MySQL, "`2fa`"},
		{"has space", MySQL, "`has space`"},
		{"订单", Postgres, `"订单"`},
		{"we`ird", MySQL, "`we``ird`"},
		{`we"ird`, Postgres, `"we""ird"`},
		{"", MySQL, "``"},
		{"range", MySQL, "`range`"},
		{"interval", MySQL, "`interval`"},
		{"rank", MySQL, "`rank`"},
		{"condition", MySQL, "`condition`"},
		{"status", MySQL, "status"},
		{"name", MySQL, "name"},
		{"range", Postgres, "range"},
		{"offset", Postgres, `"offset"`},
		{"analyze", Postgres, `"analyze"`},
		{"returning", Postgres, `"returning"`},
		{"name", Postgres, "name"},
		{"rank", Postgres, "rank"},
	} {
		if got := QuoteIfNeeded(tc.name, tc.d); got != tc.want {
			t.Errorf("QuoteIfNeeded(%q, %v) = %s, want %s", tc.name, tc.d, got, tc.want)
		}
	}
}
