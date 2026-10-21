package kmsgx

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strconv"
	"time"

	"github.com/hamba/avro/v2"

	"github.com/shepherd-yun/datavim/internal/kafka"
)

// avroSchema parses s with its references in one cache: a reference may
// need another, so they are parsed until none is left or none parses.
func avroSchema(s kafka.Schema, refs map[string]kafka.Schema) (avro.Schema, error) {
	return cached(schemaKey("avro", s, refs), func() (avro.Schema, error) {
		cache := &avro.SchemaCache{}
		left := make(map[string]kafka.Schema, len(refs))
		for n, r := range refs {
			left[n] = r
		}
		for len(left) > 0 {
			var lastErr error
			progress := false
			for n, r := range left {
				if _, err := avro.ParseWithCache(r.Text, "", cache); err != nil {
					lastErr = fmt.Errorf("reference %s: %w", n, err)
					continue
				}
				delete(left, n)
				progress = true
			}
			if !progress {
				return nil, lastErr
			}
		}
		return avro.ParseWithCache(s.Text, "", cache)
	})
}

func decodeAvro(s kafka.Schema, refs map[string]kafka.Schema, payload []byte) ([]byte, error) {
	sch, err := avroSchema(s, refs)
	if err != nil {
		return nil, err
	}
	var v any
	if err := avro.Unmarshal(sch, payload, &v); err != nil {
		return nil, err
	}
	return json.Marshal(avroToJSON(v))
}

// avroToJSON makes fixed values (Go arrays) []byte, base64 in JSON like
// bytes; json.Marshal handles the rest.
func avroToJSON(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = avroToJSON(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = avroToJSON(e)
		}
		return x
	case *big.Rat:
		return x.FloatString(18)
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Array && rv.Type().Elem().Kind() == reflect.Uint8 {
		b := make([]byte, rv.Len())
		reflect.Copy(reflect.ValueOf(b), rv)
		return b
	}
	return v
}

func encodeAvro(s kafka.Schema, refs map[string]kafka.Schema, body []byte) ([]byte, error) {
	sch, err := avroSchema(s, refs)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("the body is not JSON: %w", err)
	}
	v, err = fromJSON(sch, v, "")
	if err != nil {
		return nil, err
	}
	return avro.Marshal(sch, v)
}

// fromJSON converts v, decoded JSON, into what hamba/avro encodes for
// sch: the shapes Decode writes, so a decoded message encodes back.
func fromJSON(sch avro.Schema, v any, path string) (any, error) {
	at := func(err error) error {
		if path == "" {
			return err
		}
		return fmt.Errorf("%s: %w", path, err)
	}
	if ref, ok := sch.(*avro.RefSchema); ok {
		sch = ref.Schema()
	}
	switch sch.Type() {
	case avro.Null:
		if v != nil {
			return nil, at(errors.New("want null"))
		}
		return nil, nil
	case avro.Boolean:
		b, ok := v.(bool)
		if !ok {
			return nil, at(errors.New("want true or false"))
		}
		return b, nil
	case avro.Int, avro.Long:
		return avroInteger(sch, v, at)
	case avro.Float, avro.Double:
		n, ok := v.(json.Number)
		if !ok {
			return nil, at(errors.New("want a number"))
		}
		f, err := n.Float64()
		if err != nil {
			return nil, at(err)
		}
		if sch.Type() == avro.Float {
			return float32(f), nil
		}
		return f, nil
	case avro.String:
		str, ok := v.(string)
		if !ok {
			return nil, at(errors.New("want a string"))
		}
		return str, nil
	case avro.Bytes, avro.Fixed:
		return avroBytes(sch, v, at)
	case avro.Enum:
		str, ok := v.(string)
		if !ok {
			return nil, at(errors.New("want an enum symbol"))
		}
		return str, nil
	case avro.Array:
		arr, ok := v.([]any)
		if !ok {
			return nil, at(errors.New("want an array"))
		}
		items := sch.(*avro.ArraySchema).Items()
		out := make([]any, len(arr))
		for i, e := range arr {
			c, err := fromJSON(items, e, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	case avro.Map:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, at(errors.New("want an object"))
		}
		values := sch.(*avro.MapSchema).Values()
		out := make(map[string]any, len(m))
		for k, e := range m {
			c, err := fromJSON(values, e, join(path, k))
			if err != nil {
				return nil, err
			}
			out[k] = c
		}
		return out, nil
	case avro.Record:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, at(errors.New("want an object"))
		}
		out := make(map[string]any, len(m))
		for _, f := range sch.(*avro.RecordSchema).Fields() {
			e, present := m[f.Name()]
			if !present {
				if f.HasDefault() {
					continue
				}
				return nil, fmt.Errorf("%s: missing", join(path, f.Name()))
			}
			c, err := fromJSON(f.Type(), e, join(path, f.Name()))
			if err != nil {
				return nil, err
			}
			out[f.Name()] = c
		}
		return out, nil
	case avro.Union:
		return avroUnion(sch.(*avro.UnionSchema), v, path, at)
	}
	return nil, at(fmt.Errorf("unsupported avro type %s", sch.Type()))
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func avroInteger(sch avro.Schema, v any, at func(error) error) (any, error) {
	var logical avro.LogicalType
	if ls, ok := sch.(avro.LogicalTypeSchema); ok && ls.Logical() != nil {
		logical = ls.Logical().Type()
	}
	switch logical {
	case avro.TimestampMillis, avro.TimestampMicros, avro.LocalTimestampMillis, avro.LocalTimestampMicros, avro.Date:
		if str, ok := v.(string); ok {
			t, err := time.Parse(time.RFC3339Nano, str)
			if err != nil {
				return nil, at(err)
			}
			return t, nil
		}
	}
	n, ok := v.(json.Number)
	if !ok {
		return nil, at(errors.New("want an integer"))
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		return nil, at(fmt.Errorf("want an integer, got %s", n))
	}
	switch logical {
	case avro.TimeMillis, avro.TimeMicros:
		return time.Duration(i), nil
	}
	if sch.Type() == avro.Int {
		return int(i), nil
	}
	return i, nil
}

func avroBytes(sch avro.Schema, v any, at func(error) error) (any, error) {
	str, ok := v.(string)
	if !ok {
		return nil, at(errors.New("want base64 text"))
	}
	if ls, ok := sch.(avro.LogicalTypeSchema); ok && ls.Logical() != nil && ls.Logical().Type() == avro.Decimal {
		r, ok := new(big.Rat).SetString(str)
		if !ok {
			return nil, at(errors.New("want a decimal number"))
		}
		return r, nil
	}
	b, err := base64.StdEncoding.DecodeString(str)
	if err != nil {
		return nil, at(errors.New("want base64 text"))
	}
	if fx, ok := sch.(*avro.FixedSchema); ok {
		if len(b) != fx.Size() {
			return nil, at(fmt.Errorf("want %d bytes, got %d", fx.Size(), len(b)))
		}
		arr := reflect.New(reflect.ArrayOf(fx.Size(), reflect.TypeFor[byte]())).Elem()
		reflect.Copy(arr, reflect.ValueOf(b))
		return arr.Interface(), nil
	}
	return b, nil
}

// unionName is the key Decode writes for a union branch.
func unionName(s avro.Schema) string {
	if ref, ok := s.(*avro.RefSchema); ok {
		s = ref.Schema()
	}
	if n, ok := s.(avro.NamedSchema); ok {
		return n.FullName()
	}
	return string(s.Type())
}

// avroUnion reads {"branch": value} (Decode's shape) or null; a nullable
// union also takes the bare value.
func avroUnion(u *avro.UnionSchema, v any, path string, at func(error) error) (any, error) {
	if v == nil {
		if u.Nullable() {
			return nil, nil
		}
		return nil, at(errors.New("null is not in the union"))
	}
	if m, ok := v.(map[string]any); ok && len(m) == 1 {
		for name, e := range m {
			for _, t := range u.Types() {
				if unionName(t) == name {
					c, err := fromJSON(t, e, path)
					if err != nil {
						return nil, err
					}
					return map[string]any{name: c}, nil
				}
			}
		}
	}
	if u.Nullable() {
		for _, t := range u.Types() {
			if t.Type() != avro.Null {
				c, err := fromJSON(t, v, path)
				if err != nil {
					return nil, err
				}
				return map[string]any{unionName(t): c}, nil
			}
		}
	}
	return nil, at(errors.New(`want {"type": value} for a union`))
}
