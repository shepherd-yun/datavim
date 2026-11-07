package sqlx

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
)

func TestSQLiteLex(t *testing.T) {
	tests := []struct {
		src  string
		kind tokKind
		text string
	}{
		{`"a b"`, tokIdent, "a b"},
		{"`a b`", tokIdent, "a b"},
		{"[a b]", tokIdent, "a b"},
		{"x'00ff'", tokString, "x'00ff'"},
		{"X'00FF'", tokString, "X'00FF'"},
		{"'it''s'", tokString, "'it''s'"},
		{`'a\'`, tokString, `'a\'`}, // a backslash does not escape
		{"# not a comment", tokPunct, "#"},
		{"-- comment", tokComment, "-- comment"},
		{"/* a /* b */", tokComment, "/* a /* b */"}, // no nesting
	}
	for _, tt := range tests {
		got := lex(tt.src, 0, SQLite)
		if got.kind != tt.kind || got.text != tt.text {
			t.Errorf("lex(%q) = kind %d text %q, want kind %d text %q", tt.src, got.kind, got.text, tt.kind, tt.text)
		}
	}
	// SQLite closes [ at the first ]: there is no ]] escape
	if got := lex("[a]]", 0, SQLite); got.text != "a" || got.end != 3 {
		t.Errorf("lex([a]]) = %+v", got)
	}
}

func TestSQLiteSplitTrigger(t *testing.T) {
	tests := []struct {
		src  string
		want []string
	}{
		{
			"CREATE TRIGGER t AFTER INSERT ON q BEGIN SELECT 1; SELECT 2; END; SELECT 3;",
			[]string{"CREATE TRIGGER t AFTER INSERT ON q BEGIN SELECT 1; SELECT 2; END", "SELECT 3"},
		},
		{
			"create temp trigger t after insert on q begin select case when 1 then 2 end; end; select 3",
			[]string{"create temp trigger t after insert on q begin select case when 1 then 2 end; end", "select 3"},
		},
		{
			"-- c\nCREATE TEMPORARY TRIGGER t BEGIN SELECT 1; END ;",
			[]string{"-- c\nCREATE TEMPORARY TRIGGER t BEGIN SELECT 1; END"},
		},
		{"select 1; select ';'", []string{"select 1", "select ';'"}},
		{"create table t(a); select 2", []string{"create table t(a)", "select 2"}},
	}
	for _, tt := range tests {
		var got []string
		for _, s := range Split(tt.src, SQLite) {
			got = append(got, s.Text)
		}
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("Split(%q) (-want +got):\n%s", tt.src, diff)
		}
	}
}

func TestIsComplete(t *testing.T) {
	tests := []struct {
		src  string
		want bool
	}{
		{"", true},
		{"select 1", true},
		{"select 'a;'", true},
		{"-- c", true},
		{"/* c */", true},
		{"select 1 -- c", true},
		{"create trigger t after insert on q begin select 1; end;", true},
		{"create trigger t after insert on q begin select 1; end", true},
		{"select 'a", false},
		{`select "a`, false},
		{"select `a", false},
		{"select [a", false},
		{"select x'00", false},
		{"/* x", false},
		{"/*/", false},
		{"select 1; select 'a", false},
		{"create trigger t after insert on q begin select 1;", false},
		{"create trigger t after insert on q begin select case when 1 then 2 end;", false},
		{"create trigger t;", false},
		{"explain create trigger t after insert on q begin select 1;", false},
	}
	for _, tt := range tests {
		if got := IsComplete(tt.src, SQLite); got != tt.want {
			t.Errorf("IsComplete(%q) = %v, want %v", tt.src, got, tt.want)
		}
	}
}

func TestCallsFunction(t *testing.T) {
	hits := []string{
		"select edit(1)", "select EDIT (1)", `select "edit"(1)`, "select `edit`(1)", "select [edit](1)",
		"select edit/*c*/(1)", "select edit -- c\n(1)",
	}
	for _, s := range hits {
		if name, ok := CallsFunction(s, SQLite, "edit", "load_extension"); !ok || name != "edit" {
			t.Errorf("CallsFunction(%q) = %q, %v", s, name, ok)
		}
	}
	misses := []string{"select edit from t", "select 'edit(1)'", "-- edit(1)", "select 'edit'(1)", "select editor(1)"}
	for _, s := range misses {
		if name, ok := CallsFunction(s, SQLite, "edit", "load_extension"); ok {
			t.Errorf("CallsFunction(%q) = %q, want no hit", s, name)
		}
	}
}

func TestSQLiteAnalyze(t *testing.T) {
	reads := []string{
		"PRAGMA table_info(t)", "PRAGMA main.table_info(t)", "pragma index_list('t')", "PRAGMA user_version",
		"PRAGMA journal_mode", "PRAGMA foreign_keys", "PRAGMA integrity_check", "PRAGMA integrity_check(10)",
		"SELECT 1", "EXPLAIN QUERY PLAN SELECT 1",
	}
	for _, s := range reads {
		if got := Analyze(s, SQLite); got.Kind != Read || !got.ReturnsRows {
			t.Errorf("Analyze(%q) = %+v, want a read returning rows", s, got)
		}
	}
	writes := []string{
		"PRAGMA user_version = 1", "PRAGMA user_version(1)", "PRAGMA journal_mode=WAL", "PRAGMA journal_mode(WAL)",
		"PRAGMA main.journal_mode=WAL", "PRAGMA query_only=0", "PRAGMA wal_checkpoint", "PRAGMA optimize",
		"PRAGMA unknown_thing", "VACUUM", "REINDEX", "ATTACH 'x' AS y", "DETACH y", "ANALYZE",
	}
	for _, s := range writes {
		if got := Analyze(s, SQLite); got.Kind == Read || got.Kind == Other || got.Kind == Tx {
			t.Errorf("Analyze(%q) = %+v, want a write", s, got)
		}
	}
	for _, s := range []string{"DELETE FROM t", "UPDATE t SET a=1", "UPDATE OR REPLACE t SET a=1"} {
		got := Analyze(s, SQLite)
		if got.Kind != Write || !got.MissingWhere || got.Target != "t" {
			t.Errorf("Analyze(%q) = %+v", s, got)
		}
	}
	if got := Analyze("INSERT INTO t VALUES (1) RETURNING id", SQLite); got.Kind != Write || !got.ReturnsRows {
		t.Errorf("RETURNING: %+v", got)
	}
}

func TestSQLiteTransactionEffect(t *testing.T) {
	tests := []struct {
		src  string
		want TxEffect
	}{
		{"BEGIN", TxBegin},
		{"BEGIN IMMEDIATE", TxBegin},
		{"BEGIN EXCLUSIVE TRANSACTION", TxBegin},
		{"COMMIT", TxEnd},
		{"END", TxEnd},
		{"END TRANSACTION", TxEnd},
		{"ROLLBACK", TxEnd},
		{"ROLLBACK TO x", TxResume},
		{"SAVEPOINT x", TxBegin}, // opens a transaction when none is open
		{"SELECT 1", TxNone},
	}
	for _, tt := range tests {
		if got := TransactionEffect(tt.src, SQLite); got != tt.want {
			t.Errorf("TransactionEffect(%q) = %d, want %d", tt.src, got, tt.want)
		}
	}
	if Analyze("BEGIN IMMEDIATE", SQLite).Kind != Tx {
		t.Error("BEGIN IMMEDIATE is not Tx")
	}
}

func TestSQLiteLiteral(t *testing.T) {
	tests := []struct {
		typ  string
		v    db.Value
		want string
	}{
		{"BLOB", db.Value{Text: "x'00FF'", Blob: true}, "x'00FF'"},
		{"TEXT", db.Value{Text: "x'00FF'", Blob: true}, "x'00FF'"},
		{"TEXT", db.Value{Text: "x'00FF'"}, "'x''00FF'''"},
		{"ANY", db.Value{Text: "5"}, "5"},
		{"ANY", db.Value{Text: "abc"}, "'abc'"},
		{"INTEGER", db.Value{Text: "5"}, "5"},
		{"VARCHAR(50)", db.Value{Text: "5"}, "'5'"},
		{"BLOB", db.Value{Text: "abc"}, "'abc'"},
		{"ANY", db.Value{Null: true}, "NULL"},
		{"TEXT", db.Value{Text: `a\b`}, `'a\b'`}, // no backslash escapes
	}
	for _, tt := range tests {
		if got := Literal(SQLite, tt.typ, tt.v); got != tt.want {
			t.Errorf("Literal(%q, %+v) = %s, want %s", tt.typ, tt.v, got, tt.want)
		}
	}
}

func TestSQLiteRowID(t *testing.T) {
	if got := RowIDSelect(SQLite, `"t"`); got != `SELECT CAST(_rowid_ AS TEXT), * FROM "t"` {
		t.Error(got)
	}
	if got := RowIDMatch(SQLite, "5"); got != `_rowid_ = '5'` {
		t.Error(got)
	}
	if got := RowIDName(SQLite); got != "rowid" {
		t.Error(got)
	}
}

func TestSQLiteDialectBasics(t *testing.T) {
	if DialectOf("sqlite") != SQLite || SQLite.Driver() != "sqlite" {
		t.Error("driver name round trip")
	}
	want := Features{ServerReadOnly: true, TxDDL: true, SchemaTools: true, RowLocator: LocateRowID}
	if got := SQLite.Features(); got != want {
		t.Errorf("Features = %+v", got)
	}
	if got := QuoteIdent(SQLite, `a"b`); got != `"a""b"` {
		t.Error(got)
	}
	// a file has no namespaces, whatever the caller passes
	ref := db.TableRef{Namespace: db.Namespace{Database: "/tmp/a.db"}, Name: "t"}
	if got := QualifiedName(SQLite, ref); got != `"t"` {
		t.Error(got)
	}
	if got := BeginSQL(SQLite); got != "BEGIN" {
		t.Error(got)
	}
	if SafeOther("PRAGMA x", SQLite) {
		t.Error("SafeOther allowed a statement")
	}
	if len(Keywords(SQLite)) == 0 || !isKeyword("pragma", SQLite) {
		t.Error("keywords missing PRAGMA")
	}
	if got := QuoteIfNeeded("order", SQLite); got != `"order"` {
		t.Error(got)
	}
	if got := QuoteIfNeeded("Users", SQLite); got != "Users" {
		t.Error(got)
	}
}

func TestServerStatusFeature(t *testing.T) {
	for _, d := range []Dialect{MySQL, Postgres, SQLServer, Oracle} {
		if !d.Features().ServerStatus {
			t.Errorf("%s: ServerStatus false", d.Driver())
		}
	}
	if SQLite.Features().ServerStatus {
		t.Error("SQLite: ServerStatus true")
	}
}

func TestClosedSQLiteTokens(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"x'00'", true}, {"x'00", false}, {"X'00'", true}, {"[a]", true}, {"[a", false},
	}
	for _, tt := range tests {
		if got := closed(tt.raw); got != tt.want {
			t.Errorf("closed(%q) = %v", tt.raw, got)
		}
	}
}

func TestPlaceholders(t *testing.T) {
	got := Placeholders("select ?, '?', \"?\", /* ? */ ? -- ?\n, ?", SQLite)
	if diff := cmp.Diff([]int{7, 28, 37}, got); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}

func TestFindToken(t *testing.T) {
	src := "select 'FORM', /* FORM */ a\nFORM b"
	off, ok := FindToken(src, SQLite, "FORM")
	if !ok || src[off:off+6] != "FORM b" {
		t.Errorf("offset %d, %v", off, ok)
	}
	if _, ok := FindToken(src, SQLite, "nope"); ok {
		t.Error("found a token that is not there")
	}
}

func TestParseSQLiteTable(t *testing.T) {
	src := `CREATE TABLE "t" (
		id INTEGER CONSTRAINT pk_t PRIMARY KEY AUTOINCREMENT, -- the key, with a comma
		"Na Me" TEXT COLLATE NoCase DEFAULT 'a,b',
		parent INT CONSTRAINT fk_parent REFERENCES t(id),
		other INT REFERENCES t,
		a INT,
		b INT,
		twice INT GENERATED ALWAYS AS (a * (b + 1)) STORED,
		short INT AS (a + 1),
		CONSTRAINT fk_ab FOREIGN KEY (a, b) REFERENCES u(x, y),
		FOREIGN KEY (b) REFERENCES u(y),
		UNIQUE (a),
		CHECK (a > 0)
	) WITHOUT ROWID`
	want := SQLiteTable{
		Columns: map[string]SQLiteColumn{
			"id":     {AutoIncrement: true},
			"na me":  {Collation: "NOCASE"},
			"twice":  {Generated: "a * (b + 1)"},
			"short":  {Generated: "a + 1"},
			"parent": {},
			"other":  {},
			"a":      {},
			"b":      {},
		},
		PrimaryName: "pk_t",
		ForeignKeys: map[string]string{"parent": "fk_parent", "a,b": "fk_ab"},
	}
	if diff := cmp.Diff(want, ParseSQLiteTable(src)); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	ctas := ParseSQLiteTable("CREATE TABLE t AS SELECT 1 AS a")
	if len(ctas.Columns) != 0 {
		t.Errorf("CREATE TABLE AS: %+v", ctas)
	}
	named := ParseSQLiteTable("CREATE TABLE t (a INT, b INT, CONSTRAINT pk PRIMARY KEY (a, b))")
	if named.PrimaryName != "pk" {
		t.Errorf("table constraint: %+v", named)
	}
}

func TestParseSQLiteIndex(t *testing.T) {
	entries, where := ParseSQLiteIndex(`CREATE UNIQUE INDEX "i" ON "t" (a, lower(b), c DESC) WHERE a > 0 AND b IS NOT NULL`)
	if diff := cmp.Diff([]string{"a", "lower(b)", "c"}, entries); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if where != "a > 0 AND b IS NOT NULL" {
		t.Errorf("where = %q", where)
	}
}
