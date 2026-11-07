package sqlx

import "testing"

// TestTxTracker runs each script of successful statements; want is whether a
// transaction is open after each one.
func TestTxTracker(t *testing.T) {
	type step struct {
		sql  string
		open bool
	}
	cases := []struct {
		name  string
		d     Dialect
		steps []step
	}{
		{"a savepoint opens, releasing it commits", SQLite, []step{
			{"SAVEPOINT a", true}, {"RELEASE a", false},
		}},
		{"RELEASE SAVEPOINT, any case, quoted", SQLite, []step{
			{`savepoint "My Point"`, true}, {`release savepoint "my point";`, false},
		}},
		{"an inner release keeps the outer savepoint's transaction", SQLite, []step{
			{"SAVEPOINT a", true}, {"SAVEPOINT b", true}, {"RELEASE b", true}, {"RELEASE a", false},
		}},
		{"releasing the outer one takes the inner ones", SQLite, []step{
			{"SAVEPOINT a", true}, {"SAVEPOINT b", true}, {"RELEASE a", false},
		}},
		{"a repeated name releases the newest", SQLite, []step{
			{"SAVEPOINT a", true}, {"SAVEPOINT a", true}, {"RELEASE a", true}, {"RELEASE a", false},
		}},
		{"under BEGIN a release commits nothing", SQLite, []step{
			{"BEGIN", true}, {"SAVEPOINT a", true}, {"RELEASE a", true}, {"COMMIT", false},
		}},
		{"rolling back to a savepoint keeps it", SQLite, []step{
			{"SAVEPOINT a", true}, {"SAVEPOINT b", true}, {"ROLLBACK TO a", true}, {"RELEASE a", false},
		}},
		{"rolling back to a savepoint forgets the newer ones", SQLite, []step{
			{"SAVEPOINT a", true}, {"SAVEPOINT b", true}, {"ROLLBACK TRANSACTION TO SAVEPOINT a", true}, {"SAVEPOINT b", true}, {"RELEASE b", true}, {"RELEASE a", false},
		}},
		{"COMMIT ends a savepoint's transaction", SQLite, []step{
			{"SAVEPOINT a", true}, {"COMMIT", false}, {"BEGIN", true}, {"SAVEPOINT a", true}, {"RELEASE a", true},
		}},
		{"elsewhere RELEASE never ends a transaction", Postgres, []step{
			{"BEGIN", true}, {"SAVEPOINT a", true}, {"RELEASE a", true}, {"COMMIT", false},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var tr TxTracker
			open := false
			for _, s := range c.steps {
				switch tr.Effect(s.sql, c.d, open) {
				case TxBegin:
					open = true
				case TxEnd:
					open = false
				case TxNone, TxResume:
				}
				if open != s.open {
					t.Fatalf("after %q: open = %v, want %v", s.sql, open, s.open)
				}
			}
		})
	}
}

// A savepoint inside a transaction must not look like a fresh one: the console
// resets its count of uncommitted writes on TxBegin.
func TestTxTrackerSavepointInTxIsNotABegin(t *testing.T) {
	var tr TxTracker
	if got := tr.Effect("SAVEPOINT s", SQLite, true); got != TxNone {
		t.Fatalf("got %d, want TxNone", got)
	}
}

func TestTransactionEffect(t *testing.T) {
	cases := []struct {
		d    Dialect
		sql  string
		want TxEffect
	}{
		{MySQL, "begin", TxBegin},
		{MySQL, "BEGIN WORK", TxBegin},
		{MySQL, "start transaction read only", TxBegin},
		{Postgres, "BEGIN ISOLATION LEVEL SERIALIZABLE", TxBegin},
		{MySQL, "commit", TxEnd},
		{Postgres, "end", TxEnd},
		{Postgres, "abort", TxEnd},
		{MySQL, "/* done */ ROLLBACK", TxEnd},
		{MySQL, "commit and chain", TxBegin},
		{MySQL, "commit and no chain", TxEnd},
		{Postgres, "rollback to savepoint s1", TxResume},
		{MySQL, "ROLLBACK WORK TO s1", TxResume},
		{Postgres, "savepoint s1", TxNone},
		{Postgres, "release savepoint s1", TxNone},
		{Postgres, "prepare transaction 'x'", TxEnd},
		{Postgres, "commit prepared 'x'", TxNone},
		{MySQL, "start slave", TxNone},
		{MySQL, "select 'commit'", TxNone},
		{MySQL, "create procedure p() begin select 1; end", TxNone},
		{MySQL, "", TxNone},
	}
	for _, c := range cases {
		if got := TransactionEffect(c.sql, c.d); got != c.want {
			t.Errorf("TransactionEffect(%q) = %d, want %d", c.sql, got, c.want)
		}
	}
}
