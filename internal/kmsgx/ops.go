package kmsgx

// Op is a Kafka operation that changes the cluster.
type Op int

const (
	OpProduce Op = iota
	OpCreateTopic
	OpDeleteTopic
	OpResetOffsets
)

// Level grades an Op (design §8.1).
type Level int

const (
	LevelWrite Level = iota
	LevelDanger
)

// Confirm is how the user confirms an Op.
type Confirm int

const (
	ConfirmNone     Confirm = iota // just do it
	ConfirmYes                     // press y
	ConfirmTypeName                // type the topic or group name
)

// LevelOf grades op.
func LevelOf(op Op) Level {
	if op == OpDeleteTopic {
		return LevelDanger
	}
	return LevelWrite
}

// ConfirmFor is how op is confirmed on a prod or non-prod connection
// (design §8.2). Read-only connections refuse every Op before this.
func ConfirmFor(op Op, prod bool) Confirm {
	switch {
	case LevelOf(op) == LevelDanger:
		return ConfirmTypeName
	case op == OpResetOffsets && prod:
		return ConfirmTypeName
	case op == OpProduce && !prod:
		return ConfirmNone
	}
	return ConfirmYes
}
