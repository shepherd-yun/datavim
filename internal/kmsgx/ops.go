package kmsgx

// Op is a Kafka operation that changes the cluster.
type Op int

const (
	OpProduce Op = iota
	OpCreateTopic
	OpDeleteTopic
	OpResetOffsets
)

// K2 operations, after K1's so their values stay put.
const (
	OpAlterConfigs Op = iota + OpResetOffsets + 1
	OpAddPartitions
	OpDeleteRecords
	OpDeleteGroup
	OpDeleteOffsets
	OpRegisterSchema
	OpSetCompatibility
	OpDeleteSchemaVersion
)

// K3 operations, after K2's.
const (
	OpCreateACL Op = iota + OpDeleteSchemaVersion + 1
	OpDeleteACL
	OpPauseConnector
	OpResumeConnector
	OpRestartConnector
	OpRestartTask
	OpAlterConnectorConfig
)

var opNames = map[Op]string{
	OpProduce: "produce", OpCreateTopic: "create_topic", OpDeleteTopic: "delete_topic",
	OpResetOffsets: "reset_offsets", OpAlterConfigs: "alter_configs", OpAddPartitions: "add_partitions",
	OpDeleteRecords: "delete_records", OpDeleteGroup: "delete_group", OpDeleteOffsets: "delete_offsets",
	OpRegisterSchema: "register_schema", OpSetCompatibility: "set_compatibility",
	OpDeleteSchemaVersion: "delete_schema_version", OpCreateACL: "create_acl", OpDeleteACL: "delete_acl",
	OpPauseConnector: "pause_connector", OpResumeConnector: "resume_connector",
	OpRestartConnector: "restart_connector", OpRestartTask: "restart_task",
	OpAlterConnectorConfig: "alter_connector_config",
}

// String is the op's name in the audit log, such as "delete_topic".
func (o Op) String() string {
	if s, ok := opNames[o]; ok {
		return s
	}
	return "op_unknown"
}

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
	switch op {
	case OpDeleteTopic, OpDeleteRecords, OpDeleteGroup, OpDeleteOffsets, OpDeleteSchemaVersion, OpDeleteACL:
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
