package kafka

import "time"

// TP names one partition of a topic.
type TP struct {
	Topic     string
	Partition int32
}

// Header is one record header; Value is kept as raw bytes.
type Header struct {
	Key   string
	Value []byte
}

// Record is one message as read from a partition. Key, Value and header
// values are the raw bytes; decoding happens only for display.
type Record struct {
	Topic              string
	Partition          int32
	Offset             int64
	Time               time.Time
	TimeType           string // "CreateTime" or "LogAppendTime"
	Key, Value         []byte
	KeyNull, ValueNull bool
	Headers            []Header
	Size               int  // raw bytes of key, value and headers
	Truncated          bool // Value holds only the first ReaderConfig.MaxValueBytes
}

// Range is the offsets [From, To) of one partition.
type Range struct{ From, To int64 }
