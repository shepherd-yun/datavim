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
