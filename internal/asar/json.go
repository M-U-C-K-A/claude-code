package asar

// Ordered JSON for the asar header. Electron pins SHA-256(header JSON) in
// Info.plist, and the archive was written by JSON.stringify, so the header must
// round-trip byte for byte: keys keep their order and are written back the way
// JSON.stringify writes them (no HTML escaping, numbers verbatim).

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// object is a JSON object that remembers its key order.
type object struct {
	keys []string
	vals map[string]any
}

func newObject() *object { return &object{vals: map[string]any{}} }

func (o *object) get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// set replaces a value in place, or appends a new key at the end.
func (o *object) set(key string, value any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
}

func (o *object) child(key string) *object {
	v, _ := o.vals[key].(*object)
	return v
}

// truthy mirrors JavaScript truthiness for the header fields we test
// (`files`, `link`, `unpacked`, `integrity`).
func (o *object) truthy(key string) bool {
	switch v := o.vals[key].(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case json.Number:
		f, err := v.Float64()
		return err == nil && f != 0
	default:
		return true
	}
}

// integer reads a numeric field stored as a number or as a string (asar keeps
// `offset` as a string, `size` as a number).
func (o *object) integer(key string) (int64, error) {
	switch v := o.vals[key].(type) {
	case json.Number:
		return strconv.ParseInt(string(v), 10, 64)
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("field %q is not a number", key)
	}
}

func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the asar header JSON")
	}
	return value, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil // string, json.Number, bool or nil
	}
	switch delim {
	case '{':
		obj := newObject()
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, errors.New("invalid object key in the asar header")
			}
			value, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			obj.set(key, value)
		}
		_, err := dec.Token() // '}'
		return obj, err
	case '[':
		list := []any{}
		for dec.More() {
			value, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			list = append(list, value)
		}
		_, err := dec.Token() // ']'
		return list, err
	}
	return nil, fmt.Errorf("unexpected %v in the asar header", delim)
}

func encodeJSON(buf *bytes.Buffer, value any) {
	switch v := value.(type) {
	case *object:
		buf.WriteByte('{')
		for i, key := range v.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, key)
			buf.WriteByte(':')
			encodeJSON(buf, v.vals[key])
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			encodeJSON(buf, item)
		}
		buf.WriteByte(']')
	case string:
		writeString(buf, v)
	case json.Number:
		buf.WriteString(string(v))
	case int:
		buf.WriteString(strconv.Itoa(v))
	case int64:
		buf.WriteString(strconv.FormatInt(v, 10))
	case bool:
		buf.WriteString(strconv.FormatBool(v))
	case nil:
		buf.WriteString("null")
	default:
		panic(fmt.Sprintf("asar: cannot encode %T", value))
	}
}

// writeString quotes like JSON.stringify: only `"`, `\` and control characters
// are escaped; everything else, UTF-8 included, is written as is.
func writeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, c)
			} else {
				buf.WriteByte(c)
			}
		}
	}
	buf.WriteByte('"')
}
