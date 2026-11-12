// Package ojson edits JSON config files without reordering keys, so a file
// like ~/.claude/settings.json keeps its layout (two-space indent, original
// key order) when mom adds an entry.
package ojson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Object is a JSON object that remembers key order.
type Object struct {
	Keys []string
	Vals map[string]any // values: *Object, []any, string, json.Number, bool, nil
}

func NewObject() *Object { return &Object{Vals: map[string]any{}} }

func (o *Object) Get(k string) (any, bool) { v, ok := o.Vals[k]; return v, ok }

func (o *Object) Set(k string, v any) {
	if _, ok := o.Vals[k]; !ok {
		o.Keys = append(o.Keys, k)
	}
	o.Vals[k] = v
}

// Obj returns the object at k, creating it if missing.
func (o *Object) Obj(k string) *Object {
	if v, ok := o.Vals[k].(*Object); ok {
		return v
	}
	n := NewObject()
	o.Set(k, n)
	return n
}

// Arr returns the array at k (nil if missing).
func (o *Object) Arr(k string) []any {
	a, _ := o.Vals[k].([]any)
	return a
}

func Parse(data []byte) (*Object, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return NewObject(), nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*Object)
	if !ok {
		return nil, fmt.Errorf("top level is not an object")
	}
	return o, nil
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
			o := NewObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(kt.(string), v)
			}
			_, err := dec.Token()
			return o, err
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			_, err := dec.Token()
			return arr, err
		}
	}
	return tok, nil
}

// Encode renders like JSON.stringify(v, null, 2) plus a trailing newline.
func Encode(o *Object) []byte {
	var b bytes.Buffer
	encode(&b, o, 0)
	b.WriteString("\n")
	return b.Bytes()
}

func encode(b *bytes.Buffer, v any, depth int) {
	ind := strings.Repeat("  ", depth+1)
	switch t := v.(type) {
	case *Object:
		if len(t.Keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range t.Keys {
			b.WriteString(ind)
			writeString(b, k)
			b.WriteString(": ")
			encode(b, t.Vals[k], depth+1)
			if i < len(t.Keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(strings.Repeat("  ", depth) + "}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, x := range t {
			b.WriteString(ind)
			encode(b, x, depth+1)
			if i < len(t)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(strings.Repeat("  ", depth) + "]")
	case string:
		writeString(b, t)
	case json.Number:
		b.WriteString(t.String())
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case nil:
		b.WriteString("null")
	default:
		j, _ := json.Marshal(t)
		b.Write(j)
	}
}

func writeString(b *bytes.Buffer, s string) {
	var sb bytes.Buffer
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	b.Write(bytes.TrimRight(sb.Bytes(), "\n"))
}
