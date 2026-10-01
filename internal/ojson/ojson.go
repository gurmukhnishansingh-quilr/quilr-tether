// Package ojson parses and writes JSON while preserving object key order,
// which encoding/json's maps cannot do. Values are *Object, []any, string,
// json.Number, bool or nil.
package ojson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Object is a JSON object that remembers key order.
type Object struct {
	keys []string
	vals map[string]any
}

func NewObject() *Object { return &Object{vals: map[string]any{}} }

func (o *Object) Len() int { return len(o.keys) }

// Keys returns a copy of the keys in order.
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

func (o *Object) Get(k string) (any, bool) {
	v, ok := o.vals[k]
	return v, ok
}

func (o *Object) Has(k string) bool { _, ok := o.vals[k]; return ok }

// Set replaces the value in place, or appends a new key.
func (o *Object) Set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *Object) Delete(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, key := range o.keys {
		if key == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// GetObject returns the value at k when it is an object.
func (o *Object) GetObject(k string) (*Object, bool) {
	v, ok := o.vals[k].(*Object)
	return v, ok
}

// GetString returns the value at k when it is a string.
func (o *Object) GetString(k string) (string, bool) {
	v, ok := o.vals[k].(string)
	return v, ok
}

// Clone deep-copies the object.
func (o *Object) Clone() *Object { return Clone(o).(*Object) }

func Clone(v any) any {
	switch t := v.(type) {
	case *Object:
		c := &Object{keys: append([]string(nil), t.keys...), vals: make(map[string]any, len(t.vals))}
		for k, val := range t.vals {
			c.vals[k] = Clone(val)
		}
		return c
	case []any:
		c := make([]any, len(t))
		for i, val := range t {
			c[i] = Clone(val)
		}
		return c
	case []string:
		return append([]string(nil), t...)
	default:
		return v
	}
}

// SyntaxError carries a 1-based line and column.
type SyntaxError struct {
	Line, Col int
	Msg       string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("line %d, column %d: %s", e.Line, e.Col, e.Msg)
}

func position(data []byte, offset int64) (int, int) {
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	before := data[:offset]
	line := bytes.Count(before, []byte("\n")) + 1
	col := int(offset) - bytes.LastIndexByte(before, '\n')
	return line, col
}

// Parse decodes any JSON value, keeping object key order.
func Parse(data []byte) (any, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	wrap := func(err error) error {
		var se *json.SyntaxError
		if errors.As(err, &se) {
			l, c := position(data, se.Offset)
			return &SyntaxError{l, c, se.Error()}
		}
		l, c := position(data, dec.InputOffset())
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return &SyntaxError{l, c, "unexpected end of input"}
		}
		return &SyntaxError{l, c, err.Error()}
	}
	v, err := parseValue(dec)
	if err != nil {
		return nil, wrap(err)
	}
	if _, err := dec.Token(); err != io.EOF {
		l, c := position(data, dec.InputOffset())
		return nil, &SyntaxError{l, c, "unexpected data after the top-level value"}
	}
	return v, nil
}

// ParseObject parses a document that must be a JSON object.
func ParseObject(data []byte) (*Object, error) {
	v, err := Parse(data)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*Object)
	if !ok {
		return nil, fmt.Errorf("expected a JSON object, found %s", typeName(v))
	}
	return o, nil
}

func typeName(v any) string {
	switch v.(type) {
	case []any:
		return "an array"
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case bool:
		return "a boolean"
	case nil:
		return "null"
	}
	return "an object"
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := NewObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				val, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				obj.Set(key, val)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				val, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return tok, nil
	}
}

// Marshal writes v as JSON. indent "" means compact.
func Marshal(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeValue(&buf, v); err != nil {
		return nil, err
	}
	if indent == "" {
		return buf.Bytes(), nil
	}
	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", indent); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeString(buf *bytes.Buffer, s string) {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	buf.Truncate(buf.Len() - 1) // Encode appends a newline
}

func writeValue(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case *Object:
		buf.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeString(buf, k)
			buf.WriteByte(':')
			if err := writeValue(buf, t.vals[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeValue(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case []string:
		arr := make([]any, len(t))
		for i, s := range t {
			arr[i] = s
		}
		return writeValue(buf, arr)
	case map[string]string:
		// Plain maps have no order; sort for stable output.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		obj := NewObject()
		for _, k := range keys {
			obj.Set(k, t[k])
		}
		return writeValue(buf, obj)
	case string:
		writeString(buf, t)
	case json.Number:
		buf.WriteString(t.String())
	case bool, int, int64, float64:
		b, _ := json.Marshal(t)
		buf.Write(b)
	case nil:
		buf.WriteString("null")
	default:
		return fmt.Errorf("ojson: cannot marshal %T", v)
	}
	return nil
}

// Normalize converts []string and map[string]string into ojson values so that
// Equal compares like with like.
func Normalize(v any) any {
	b, err := Marshal(v, "")
	if err != nil {
		return v
	}
	n, err := Parse(b)
	if err != nil {
		return v
	}
	return n
}

// Equal compares two values; object key order is ignored.
func Equal(a, b any) bool { return equal(Normalize(a), Normalize(b)) }

func equal(a, b any) bool {
	switch x := a.(type) {
	case *Object:
		y, ok := b.(*Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for k, v := range x.vals {
			w, ok := y.vals[k]
			if !ok || !equal(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}

// DetectIndent returns the indent unit used by an existing document.
func DetectIndent(data []byte) string {
	lines := strings.Split(string(data), "\n")
	for _, line := range lines[min(1, len(lines)):] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && trimmed != line {
			ws := line[:len(line)-len(trimmed)]
			if strings.HasPrefix(ws, "\t") {
				return "\t"
			}
			return ws
		}
	}
	return "  "
}
