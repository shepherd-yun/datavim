package sqlx

// TxEffect is what a statement does to the session's transaction.
type TxEffect int

const (
	TxNone   TxEffect = iota
	TxBegin           // a fresh transaction is open afterwards: BEGIN, START TRANSACTION, COMMIT AND CHAIN
	TxEnd             // the transaction is over: COMMIT, ROLLBACK, END, ABORT, PREPARE TRANSACTION
	TxResume          // ROLLBACK TO SAVEPOINT: still open, and usable again after an error
)

// TransactionEffect classifies transaction-control statements; every other
// statement is TxNone. SAVEPOINT and RELEASE leave the transaction as it is.
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
