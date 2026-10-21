package kmsgx

import (
	"context"
	"errors"
	"fmt"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

// protoFile compiles s, as main.proto, with its references as the files
// they are imported by (a reference's name is its file name).
func protoFile(s kafka.Schema, refs map[string]kafka.Schema) (protoreflect.FileDescriptor, error) {
	return cached(schemaKey("proto", s, refs), func() (protoreflect.FileDescriptor, error) {
		srcs := map[string]string{"main.proto": s.Text}
		for n, r := range refs {
			srcs[n] = r.Text
		}
		c := protocompile.Compiler{Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			Accessor: protocompile.SourceAccessorFromMap(srcs),
		})}
		files, err := c.Compile(context.Background(), "main.proto")
		if err != nil {
			return nil, err
		}
		return files[0], nil
	})
}

// readIndexes reads Confluent's message indexes: a zigzag varint count,
// then each index; a count of 0 is [0], the first message.
func readIndexes(b []byte) ([]int, []byte, error) {
	errIdx := errors.New("bad message index")
	n, l := protowire.ConsumeVarint(b)
	if l < 0 {
		return nil, nil, errIdx
	}
	b = b[l:]
	count := protowire.DecodeZigZag(n)
	if count == 0 {
		return []int{0}, b, nil
	}
	if count < 0 || count > 64 {
		return nil, nil, errIdx
	}
	idx := make([]int, count)
	for i := range idx {
		v, l := protowire.ConsumeVarint(b)
		if l < 0 {
			return nil, nil, errIdx
		}
		b = b[l:]
		idx[i] = int(protowire.DecodeZigZag(v))
	}
	return idx, b, nil
}

func appendIndexes(b []byte, idx []int) []byte {
	if len(idx) == 1 && idx[0] == 0 {
		return append(b, 0)
	}
	b = protowire.AppendVarint(b, protowire.EncodeZigZag(int64(len(idx))))
	for _, i := range idx {
		b = protowire.AppendVarint(b, protowire.EncodeZigZag(int64(i)))
	}
	return b
}

func messageAt(fd protoreflect.FileDescriptor, idx []int) (protoreflect.MessageDescriptor, error) {
	msgs := fd.Messages()
	var md protoreflect.MessageDescriptor
	for _, i := range idx {
		if i < 0 || i >= msgs.Len() {
			return nil, fmt.Errorf("message index %v is not in the schema", idx)
		}
		md = msgs.Get(i)
		msgs = md.Messages()
	}
	if md == nil {
		return nil, errors.New("the schema has no message")
	}
	return md, nil
}

// findMessage is the message called name and its indexes; "" is the first.
func findMessage(fd protoreflect.FileDescriptor, name string) (protoreflect.MessageDescriptor, []int, error) {
	if name == "" {
		md, err := messageAt(fd, []int{0})
		return md, []int{0}, err
	}
	var walk func(msgs protoreflect.MessageDescriptors, path []int) (protoreflect.MessageDescriptor, []int)
	walk = func(msgs protoreflect.MessageDescriptors, path []int) (protoreflect.MessageDescriptor, []int) {
		for i := range msgs.Len() {
			md := msgs.Get(i)
			p := append(append([]int(nil), path...), i)
			if string(md.FullName()) == name {
				return md, p
			}
			if m, mp := walk(md.Messages(), p); m != nil {
				return m, mp
			}
		}
		return nil, nil
	}
	if md, p := walk(fd.Messages(), nil); md != nil {
		return md, p, nil
	}
	return nil, nil, fmt.Errorf("no message %s in the schema", name)
}

func decodeProto(s kafka.Schema, refs map[string]kafka.Schema, payload []byte) ([]byte, error) {
	fd, err := protoFile(s, refs)
	if err != nil {
		return nil, err
	}
	idx, rest, err := readIndexes(payload)
	if err != nil {
		return nil, err
	}
	md, err := messageAt(fd, idx)
	if err != nil {
		return nil, err
	}
	msg := dynamicpb.NewMessage(md)
	if err := proto.Unmarshal(rest, msg); err != nil {
		return nil, err
	}
	out, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return compactJSON(out) // protojson varies its spacing on purpose
}

func encodeProto(s kafka.Schema, refs map[string]kafka.Schema, message string, body []byte) ([]byte, error) {
	fd, err := protoFile(s, refs)
	if err != nil {
		return nil, err
	}
	md, idx, err := findMessage(fd, message)
	if err != nil {
		return nil, err
	}
	msg := dynamicpb.NewMessage(md)
	if err := protojson.Unmarshal(body, msg); err != nil {
		return nil, err
	}
	b, err := proto.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return append(appendIndexes(nil, idx), b...), nil
}
