package sqlx

import "testing"

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
