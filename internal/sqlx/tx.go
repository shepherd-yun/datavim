package sqlx

import "strings"

// TxEffect is what a statement does to the session's transaction.
type TxEffect int

const (
	TxNone   TxEffect = iota
	TxBegin           // a fresh transaction is open afterwards: BEGIN, START TRANSACTION, COMMIT AND CHAIN
	TxEnd             // the transaction is over: COMMIT, ROLLBACK, END, ABORT, PREPARE TRANSACTION
	TxResume          // ROLLBACK TO SAVEPOINT: still open, and usable again after an error
)

// TransactionEffect classifies transaction-control statements; every other
// statement is TxNone. SAVEPOINT and RELEASE leave the transaction as it is,
// except on SQLite, where SAVEPOINT counts as opening one and only TxTracker
// can tell what it and RELEASE really do.
func TransactionEffect(stmt string, d Dialect) TxEffect {
	toks := codeTokens(stmt, d)
	if len(toks) == 0 || toks[0].kind != tokWord {
		return TxNone
	}
	if d == SQLServer {
		return tsqlTransactionEffect(toks)
	}
	if d == Oracle {
		return oracleTransactionEffect(toks)
	}
	if d == SQLite && isWord(toks[0], "SAVEPOINT") {
		return TxBegin // outside a transaction a SAVEPOINT opens one
	}
	switch upper(toks[0]) {
	case "BEGIN":
		return TxBegin
	case "START":
		if len(toks) > 1 && isWord(toks[1], "TRANSACTION") {
			return TxBegin
		}
	case "PREPARE":
		if len(toks) > 1 && isWord(toks[1], "TRANSACTION") {
			return TxEnd
		}
	case "ROLLBACK":
		if indexWord(toks, 1, "TO") >= 0 {
			return TxResume
		}
		return endEffect(toks)
	case "COMMIT", "END", "ABORT":
		return endEffect(toks)
	}
	return TxNone
}

// TxTracker follows what TransactionEffect cannot see from one statement. On
// SQLite a SAVEPOINT outside a transaction opens one, which releasing that
// savepoint commits. The zero value is ready; other dialects need no state.
type TxTracker struct {
	savepoints []string
	implicit   bool // the first of savepoints opened the transaction
}

// Effect is what stmt, which has just succeeded, did to the transaction;
// inTx says whether one was open before it.
func (tr *TxTracker) Effect(stmt string, d Dialect, inTx bool) TxEffect {
	effect := TransactionEffect(stmt, d)
	if d != SQLite {
		return effect
	}
	toks := codeTokens(stmt, d)
	if n := len(toks); n > 0 && isPunct(toks[n-1], ";") {
		toks = toks[:n-1]
	}
	if len(toks) == 0 {
		return effect
	}
	name := strings.Trim(toks[len(toks)-1].text, "'")
	switch {
	case isWord(toks[0], "SAVEPOINT"):
		if inTx {
			tr.savepoints = append(tr.savepoints, name)
			return TxNone
		}
		tr.savepoints, tr.implicit = []string{name}, true
	case isWord(toks[0], "RELEASE"):
		i := tr.find(name)
		if i < 0 {
			return effect
		}
		tr.savepoints = tr.savepoints[:i]
		if i == 0 && tr.implicit {
			*tr = TxTracker{}
			return TxEnd
		}
	case effect == TxResume:
		if i := tr.find(name); i >= 0 {
			tr.savepoints = tr.savepoints[:i+1]
		}
	case effect == TxBegin, effect == TxEnd:
		*tr = TxTracker{}
	}
	return effect
}

// find is the newest savepoint called name, or -1.
func (tr *TxTracker) find(name string) int {
	for i := len(tr.savepoints) - 1; i >= 0; i-- {
		if strings.EqualFold(tr.savepoints[i], name) {
			return i
		}
	}
	return -1
}

func endEffect(toks []token) TxEffect {
	if indexWord(toks, 1, "PREPARED") >= 0 {
		return TxNone // two-phase commit of another, already prepared transaction
	}
	if k := indexWord(toks, 1, "CHAIN"); k > 0 && !isWord(toks[k-1], "NO") {
		return TxBegin
	}
	return TxEnd
}

// tsqlTransactionEffect classifies T-SQL: BEGIN alone opens a block, END
// closes one, and a named ROLLBACK TRAN goes back to a SAVE TRAN point.
func tsqlTransactionEffect(toks []token) TxEffect {
	tran := func(t token) bool { return isWord(t, "TRAN") || isWord(t, "TRANSACTION") }
	switch upper(toks[0]) {
	case "BEGIN":
		if len(toks) > 1 && (tran(toks[1]) || isWord(toks[1], "DISTRIBUTED")) {
			return TxBegin
		}
	case "COMMIT":
		return TxEnd
	case "ROLLBACK":
		if len(toks) > 2 && tran(toks[1]) {
			return TxResume
		}
		return TxEnd
	}
	return TxNone
}

// oracleTransactionEffect classifies Oracle's statements: SET TRANSACTION
// opens a transaction explicitly; BEGIN and END belong to PL/SQL blocks.
func oracleTransactionEffect(toks []token) TxEffect {
	switch upper(toks[0]) {
	case "SET":
		if len(toks) > 1 && isWord(toks[1], "TRANSACTION") {
			return TxBegin
		}
	case "COMMIT":
		return TxEnd
	case "ROLLBACK":
		if indexWord(toks, 1, "TO") >= 0 {
			return TxResume
		}
		return TxEnd
	}
	return TxNone
}

// SavepointSQL sets savepoint name in the open transaction.
func SavepointSQL(d Dialect, name string) string {
	if d == SQLServer {
		return "SAVE TRANSACTION " + name
	}
	return "SAVEPOINT " + name
}

// RollbackToSQL undoes the transaction's work after savepoint name.
func RollbackToSQL(d Dialect, name string) string {
	if d == SQLServer {
		return "ROLLBACK TRANSACTION " + name
	}
	return "ROLLBACK TO SAVEPOINT " + name
}

// ReleaseSQL forgets savepoint name; "" where the engine has no RELEASE
// (SQL Server, Oracle) and savepoints simply end with the transaction.
func ReleaseSQL(d Dialect, name string) string {
	if d == SQLServer || d == Oracle {
		return ""
	}
	return "RELEASE SAVEPOINT " + name
}
