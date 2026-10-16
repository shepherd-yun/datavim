package sqlx

import "testing"

func TestFeatures(t *testing.T) {
	tests := []struct {
		d    Dialect
		want Features
	}{
		{MySQL, Features{Databases: true, ServerReadOnly: true, DDLCommits: true, SchemaTools: true, RowLocator: LocateAllColumns}},
		{Postgres, Features{Databases: true, Schemas: true, ServerReadOnly: true, TxDDL: true, AbortedTx: true, SchemaTools: true, ILike: true, RowLocator: LocateRowID, DefaultSchema: "public"}},
		{SQLServer, Features{Databases: true, Schemas: true, TxDDL: true, RowLocator: LocateAllColumns, DefaultSchema: "dbo"}},
		{Oracle, Features{Schemas: true, DDLCommits: true, RowLocator: LocateRowID}},
	}
	for _, tt := range tests {
		if got := tt.d.Features(); got != tt.want {
			t.Errorf("%s: Features() = %+v, want %+v", tt.d.Driver(), got, tt.want)
		}
	}
}

func TestDialectRoundTrip(t *testing.T) {
	for _, name := range []string{"mysql", "postgres", "sqlserver", "oracle"} {
		d := DialectOf(name)
		if d.Driver() != name {
			t.Errorf("DialectOf(%q).Driver() = %q", name, d.Driver())
		}
		if DialectOf(d.Driver()) != d {
			t.Errorf("round trip of %q failed", name)
		}
	}
}
