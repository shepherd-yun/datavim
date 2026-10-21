package ui

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/kmsgx"
	"github.com/shepherd-yun/datavim/internal/session"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// recID names one message of a topic tab.
type recID struct {
	Partition int32
	Offset    int64
}

// decodedPart is a key or value decoded with the registry.
type decodedPart struct {
	JSON   []byte
	Schema kafka.Schema
	Err    error // the schema could not be read or the bytes not decoded
}

// decodedRec is a message's key and value decoded; nil: not in the wire format.
type decodedRec struct{ Key, Value *decodedPart }

// msgsDecodedMsg is a decodeCmd's outcome for the view load gen.
type msgsDecodedMsg struct {
	tabID int
	gen   int
	conn  string
	dec   map[recID]decodedRec
	err   error
}

func (m msgsDecodedMsg) TabID() int               { return m.tabID }
func (m msgsDecodedMsg) kafkaMsg()                {}
func (m msgsDecodedMsg) failure() (string, error) { return m.conn, m.err }

// hasRegistry reports whether the tab's connection has a Schema Registry.
func (t *topicTab) hasRegistry() bool {
	cfg, _ := t.backend.Connection(t.conn)
	return cfg.Params["schema_registry_url"] != ""
}

func inWire(b []byte, null bool) bool {
	_, _, ok := kmsgx.Wire(b)
	return ok && !null
}

// toDecode is the recs with a wire-format key or value not decoded yet.
func (t *topicTab) toDecode(recs []kafka.Record) []kafka.Record {
	if !t.hasRegistry() {
		return nil
	}
	var out []kafka.Record
	for _, r := range recs {
		if _, done := t.msgs.decoded[recID{r.Partition, r.Offset}]; done {
			continue
		}
		if inWire(r.Key, r.KeyNull) || inWire(r.Value, r.ValueNull) {
			out = append(out, r)
		}
	}
	return out
}

// decodeCmd decodes the wire-format keys and values of recs off the UI
// goroutine; a record's failure never stops the others.
func (t *topicTab) decodeCmd(recs []kafka.Record) tea.Cmd {
	recs = t.toDecode(recs)
	if len(recs) == 0 {
		return nil
	}
	tabCtx, id, gen, b, conn := t.ctx, t.id, t.msgs.gen, t.backend, t.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		msg := msgsDecodedMsg{tabID: id, gen: gen, conn: conn}
		msg.dec, msg.err = decodeRecs(ctx, b, conn, recs)
		return msg
	}
}

// decodeRecs decodes recs with conn's registry. Only a registry that
// wants a password fails it all: the user is asked for it.
func decodeRecs(ctx context.Context, b Backend, conn string, recs []kafka.Record) (map[recID]decodedRec, error) {
	if len(recs) == 0 {
		return nil, nil
	}
	reg, err := b.SchemaRegistry(ctx, conn)
	if errors.Is(err, session.ErrNoRegistry) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type found struct {
		s    kafka.Schema
		refs map[string]kafka.Schema
		err  error
	}
	schemas := map[int]found{}
	var askErr error
	schema := func(id int) found {
		if f, ok := schemas[id]; ok {
			return f
		}
		var f found
		f.s, f.err = reg.SchemaByID(ctx, id)
		if f.err == nil {
			f.refs, f.err = reg.Resolve(ctx, f.s)
		}
		var se *session.SecretRequiredError
		if errors.As(f.err, &se) {
			askErr = f.err
		}
		schemas[id] = f
		return f
	}
	part := func(v []byte, null bool) (out *decodedPart) {
		id, payload, ok := kmsgx.Wire(v)
		if !ok || null {
			return nil
		}
		f := schema(id)
		if f.err != nil {
			return &decodedPart{Err: f.err}
		}
		defer func() {
			if r := recover(); r != nil {
				out = &decodedPart{Schema: f.s, Err: fmt.Errorf("schema %d: can't decode: %v", id, r)}
			}
		}()
		js, err := kmsgx.Decode(f.s, f.refs, payload)
		if err != nil {
			return &decodedPart{Schema: f.s, Err: fmt.Errorf("schema %d: %w", id, err)}
		}
		return &decodedPart{JSON: js, Schema: f.s}
	}
	out := make(map[recID]decodedRec, len(recs))
	for _, r := range recs {
		out[recID{r.Partition, r.Offset}] = decodedRec{Key: part(r.Key, r.KeyNull), Value: part(r.Value, r.ValueNull)}
		if askErr != nil {
			return nil, askErr
		}
	}
	return out, nil
}

// decodedRecord is r with Key / Value replaced by their decoded JSON,
// for previews, filters and searches.
func (v *msgView) decodedRecord(r kafka.Record) kafka.Record {
	d, ok := v.decoded[recID{r.Partition, r.Offset}]
	if !ok {
		return r
	}
	if d.Key != nil && d.Key.Err == nil {
		r.Key = d.Key.JSON
	}
	if d.Value != nil && d.Value.Err == nil {
		r.Value = d.Value.JSON
	}
	return r
}

// decodedOf is r's decoded part (value or key) when the view shows
// decoded bytes: format auto.
func (v *msgView) decodedOf(r kafka.Record, key bool) *decodedPart {
	if v.format != fmtAuto {
		return nil
	}
	d := v.decoded[recID{r.Partition, r.Offset}]
	if key {
		return d.Key
	}
	return d.Value
}

// partPreview is a key or value cell: the decoded JSON, the bytes in hex
// when decoding failed, else K1's preview.
func (v *msgView) partPreview(r kafka.Record, key bool) string {
	b, null := r.Value, r.ValueNull
	if key {
		b, null = r.Key, r.KeyNull
	}
	switch d := v.decodedOf(r, key); {
	case d == nil:
		return kmsgx.Preview(b, null, previewWidth)
	case d.Err != nil && v.isMasked(r):
		return kmsgx.Masked // its bytes are gone: the error is in the detail
	case d.Err != nil:
		return kmsgx.Preview([]byte(hex.EncodeToString(b)), false, previewWidth)
	default:
		return kmsgx.Preview(d.JSON, false, previewWidth)
	}
}

// failed reports whether r's key or value did not decode.
func (v *msgView) failed(r kafka.Record) bool {
	k, val := v.decodedOf(r, true), v.decodedOf(r, false)
	return (k != nil && k.Err != nil) || (val != nil && val.Err != nil)
}

// prettyJSON indents js; other text is returned as it is.
func prettyJSON(js []byte) string {
	var out bytes.Buffer
	if err := json.Indent(&out, js, "", "  "); err != nil {
		return string(js)
	}
	return out.String()
}

// schemaLine is "schema id 12 · orders-value · v3 · AVRO".
func schemaLine(s kafka.Schema) string {
	line := fmt.Sprintf("schema id %d", s.ID)
	if s.Subject != "" {
		line += fmt.Sprintf(" · %s · v%d", s.Subject, s.Version)
	}
	return line + " · " + s.Type
}

// decodedValueLines are the detail's lines above and of a decoded value;
// ok is false when the value is not decoded.
func (v *msgView) decodedValueLines(r kafka.Record) (head string, body []string, used valueFormat, ok bool) {
	d := v.decodedOf(r, false)
	if d == nil {
		return "", nil, fmtAuto, false
	}
	if d.Err != nil && v.isMasked(r) {
		return theme.Error.Render(d.Err.Error()), []string{kmsgx.Masked}, fmtText, true
	}
	if d.Err != nil {
		body, _ = renderBytes(r.Value, false, fmtHex)
		return theme.Error.Render(d.Err.Error()), body, fmtHex, true
	}
	body, used = renderValue(string(d.JSON), fmtJSON)
	return theme.Muted.Render(schemaLine(d.Schema)), body, used, true
}

// decodedUpdate applies a decodeCmd's outcome.
func (t *topicTab) decodedUpdate(m msgsDecodedMsg) tea.Cmd {
	v := t.msgs
	if m.gen != v.gen {
		return nil
	}
	if m.err != nil {
		t.setStatus(kafkaErr(m.err), true)
		return nil
	}
	if v.decoded == nil {
		v.decoded = map[recID]decodedRec{}
	}
	t.maskDecoded(m.dec)
	for id, d := range m.dec {
		v.decoded[id] = d
	}
	t.renderMsgs(v.recIndex())
	return nil
}
