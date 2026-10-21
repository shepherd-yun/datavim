package ui

import (
	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/kafka"
	"github.com/shepherd-yun/datavim/internal/kmsgx"
)

// SetMasks sets the [[mask]] rules of config.toml; topic tabs opened
// after it use them.
func (m *Model) SetMasks(rules []config.MaskRule) { m.masks = rules }

// maskerFor is the Masker of a topic tab on conn.
func (m *Model) maskerFor(conn, topic string) *kmsgx.Masker {
	cfg, _ := m.backend.Connection(conn)
	return kmsgx.NewMasker(m.masks, conn, string(cfg.Env), topic)
}

// maskedPart is a part waiting for its decoded JSON: shown as Masked.
var maskedPart = []byte(`"` + kmsgx.Masked + `"`)

// maskIn masks recs as they enter the view, so nothing past this point
// sees what the rules hide. dec is their decoding when known (a search
// decodes before it returns); otherwise a wire-format part a rule covers
// is Masked until decodedUpdate masks its decoded JSON.
// Parts in the wire format lose their bytes, so they count as masked.
func (t *topicTab) maskIn(recs []kafka.Record, dec map[recID]decodedRec) []kafka.Record {
	v := t.msgs
	mk := v.masker
	if mk == nil || len(recs) == 0 {
		return recs
	}
	if v.masked == nil {
		v.masked, v.pending = map[recID]bool{}, map[recID][2]bool{}
	}
	reg := t.hasRegistry()
	out := make([]kafka.Record, len(recs))
	for i, r := range recs {
		id := recID{r.Partition, r.Offset}
		d, known := dec[id]
		// a wire part a rule covers is decoded later, from the raw bytes:
		// that decode must be masked too, whole rule or path
		wireKey := reg && mk.Covers(true) && inWire(r.Key, r.KeyNull)
		wireValue := reg && mk.Covers(false) && inWire(r.Value, r.ValueNull)
		probe := r // the wire parts are done below; the rest, and headers, here
		if wireKey {
			probe.Key = []byte("null")
		}
		if wireValue {
			probe.Value = []byte("null")
		}
		o, masked := mk.Mask(probe, nil, nil)
		if wireKey {
			o.Key = wirePart(mk, true, d.Key, known)
			d.Key = maskedDecoded(o.Key, d.Key)
		}
		if wireValue {
			o.Value = wirePart(mk, false, d.Value, known)
			d.Value = maskedDecoded(o.Value, d.Value)
		}
		if known {
			dec[id] = d
		}
		if wireKey || wireValue {
			masked = true
			if !known {
				v.pending[id] = [2]bool{wireKey, wireValue}
			}
		}
		if masked {
			v.masked[id] = true
		}
		out[i] = o
	}
	return out
}

// wirePart is a wire-format part: its decoded JSON masked, or Masked when
// it is not decoded (yet, or at all).
func wirePart(mk *kmsgx.Masker, key bool, d *decodedPart, known bool) []byte {
	if !known || d == nil || d.Err != nil {
		return maskedPart
	}
	js, _ := mk.MaskJSON(key, d.JSON)
	return js
}

// maskedDecoded is d with its JSON replaced by the masked part.
func maskedDecoded(part []byte, d *decodedPart) *decodedPart {
	if d == nil || d.Err != nil {
		return d
	}
	c := *d
	c.JSON = part
	return &c
}

// maskDecoded masks what a decodeCmd brought for the parts waiting for it.
func (t *topicTab) maskDecoded(dec map[recID]decodedRec) {
	v := t.msgs
	if v.masker == nil {
		return
	}
	for id, d := range dec {
		p, ok := v.pending[id]
		if !ok {
			continue
		}
		delete(v.pending, id)
		i := v.indexOf(id)
		if p[0] {
			part := wirePart(v.masker, true, d.Key, true)
			d.Key = maskedDecoded(part, d.Key)
			if i >= 0 {
				v.recs[i].Key = part
			}
		}
		if p[1] {
			part := wirePart(v.masker, false, d.Value, true)
			d.Value = maskedDecoded(part, d.Value)
			if i >= 0 {
				v.recs[i].Value = part
			}
		}
		dec[id] = d
	}
}

// indexOf is the index in recs of the message id, or -1.
func (v *msgView) indexOf(id recID) int {
	for i, r := range v.recs {
		if r.Partition == id.Partition && r.Offset == id.Offset {
			return i
		}
	}
	return -1
}

// isMasked reports whether rules hid part of r.
func (v *msgView) isMasked(r kafka.Record) bool { return v.masked[recID{r.Partition, r.Offset}] }
