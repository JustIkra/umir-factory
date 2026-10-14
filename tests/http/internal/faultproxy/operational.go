package faultproxy

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed protocol.schema.json
var protocolFiles embed.FS

var ErrProtocol = errors.New("invalid operational protocol")

func StrictJSON(data []byte, depth int) (any, error) {
	if !utf8.Valid(data) || depth < 1 {
		return nil, ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(level int) (any, error) {
		token, err := d.Token()
		if err != nil {
			return nil, ErrProtocol
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return token, nil
		}
		if level >= depth {
			return nil, ErrProtocol
		}
		switch delim {
		case '{':
			obj := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, ErrProtocol
				}
				key, yes := k.(string)
				if !yes {
					return nil, ErrProtocol
				}
				if _, dup := obj[key]; dup {
					return nil, ErrProtocol
				}
				v, e := read(level + 1)
				if e != nil {
					return nil, e
				}
				obj[key] = v
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, ErrProtocol
			}
			return obj, nil
		case '[':
			arr := []any{}
			for d.More() {
				v, e := read(level + 1)
				if e != nil {
					return nil, e
				}
				arr = append(arr, v)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, ErrProtocol
			}
			return arr, nil
		}
		return nil, ErrProtocol
	}
	v, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrProtocol
	}
	return v, nil
}

func ValidateOperational(ref string, data []byte) error {
	raw, err := protocolFiles.ReadFile("protocol.schema.json")
	if err != nil {
		return ErrProtocol
	}
	return ValidateSchema(raw, ref, data, 16)
}

func ValidateSchema(schema []byte, ref string, data []byte, depth int) error {
	root, err := StrictJSON(schema, 64)
	if err != nil {
		return ErrProtocol
	}
	v, err := StrictJSON(data, depth)
	if err != nil {
		return err
	}
	s := resolveSchema(root, ref)
	if s == nil {
		return ErrProtocol
	}
	if !schemaValid(root, s, v, 0) {
		return ErrProtocol
	}
	return nil
}

func resolveSchema(root any, ref string) any {
	if ref == "" || ref == "#" {
		return root
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	v := root
	for _, part := range strings.Split(ref[2:], "/") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")]
	}
	return v
}

func schemaValid(root, schema, value any, level int) bool {
	if level > 64 {
		return false
	}
	if b, ok := schema.(bool); ok {
		return b
	}
	s, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	if ref, ok := s["$ref"].(string); ok && !schemaValid(root, resolveSchema(root, ref), value, level+1) {
		return false
	}
	if val, ok := s["const"]; ok && !reflect.DeepEqual(value, val) {
		return false
	}
	if list, ok := s["enum"].([]any); ok {
		found := false
		for _, v := range list {
			if reflect.DeepEqual(value, v) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	for _, kind := range []string{"oneOf", "anyOf", "allOf"} {
		if list, ok := s[kind].([]any); ok {
			matches := 0
			for _, v := range list {
				if schemaValid(root, v, value, level+1) {
					matches++
				}
			}
			if (kind == "oneOf" && matches != 1) || (kind == "anyOf" && matches == 0) || (kind == "allOf" && matches != len(list)) {
				return false
			}
		}
	}
	if val, ok := s["not"]; ok && schemaValid(root, val, value, level+1) {
		return false
	}
	if val, ok := s["if"]; ok {
		next := "else"
		if schemaValid(root, val, value, level+1) {
			next = "then"
		}
		if t, ok := s[next]; ok && !schemaValid(root, t, value, level+1) {
			return false
		}
	}
	if t, ok := s["type"].(string); ok {
		valid := false
		switch t {
		case "object":
			_, valid = value.(map[string]any)
		case "array":
			_, valid = value.([]any)
		case "string":
			_, valid = value.(string)
		case "boolean":
			_, valid = value.(bool)
		case "null":
			valid = value == nil
		case "number", "integer":
			n, yes := value.(json.Number)
			if yes {
				f, e := n.Float64()
				valid = e == nil && (t != "integer" || math.Trunc(f) == f)
			}
		}
		if !valid {
			return false
		}
	}
	switch v := value.(type) {
	case map[string]any:
		if req, ok := s["required"].([]any); ok {
			for _, k := range req {
				key, yes := k.(string)
				if !yes {
					return false
				}
				if _, yes = v[key]; !yes {
					return false
				}
			}
		}
		props, propsOK := s["properties"].(map[string]any)
		if !propsOK {
			props = map[string]any{}
		}
		for k, val := range v {
			if pn, yes := s["propertyNames"]; yes && !schemaValid(root, pn, k, level+1) {
				return false
			}
			if sub, yes := props[k]; yes {
				if !schemaValid(root, sub, val, level+1) {
					return false
				}
			} else if sub, yes := s["additionalProperties"]; yes && !schemaValid(root, sub, val, level+1) {
				return false
			}
		}
	case []any:
		if !schemaBounds(s, "minItems", "maxItems", float64(len(v))) {
			return false
		}
		if unique, ok := s["uniqueItems"].(bool); ok && unique {
			for i, x := range v {
				for _, y := range v[:i] {
					if reflect.DeepEqual(x, y) {
						return false
					}
				}
			}
		}
		if sub, yes := s["items"]; yes {
			for _, x := range v {
				if !schemaValid(root, sub, x, level+1) {
					return false
				}
			}
		}
	case string:
		if !schemaBounds(s, "minLength", "maxLength", float64(utf8.RuneCountInString(v))) {
			return false
		}
		if pat, yes := s["pattern"].(string); yes {
			if pat == "^(?!fixture-).+$" {
				if v == "" || strings.HasPrefix(v, "fixture-") {
					return false
				}
			} else {
				r, e := regexp.Compile(pat)
				if e != nil || !r.MatchString(v) {
					return false
				}
			}
		}
		if format, yes := s["format"].(string); yes {
			switch format {
			case "date-time":
				if !validInstant(v) {
					return false
				}
			case "uuid":
				if !UUID(v) {
					return false
				}
			case "date":
				t, e := time.Parse("2006-01-02", v)
				if e != nil || t.Format("2006-01-02") != v {
					return false
				}
			case "email":
				if !strings.Contains(v, "@") {
					return false
				}
			default:
				return false
			}
		}
	case json.Number:
		f, e := v.Float64()
		if e != nil || !schemaBounds(s, "minimum", "maximum", f) {
			return false
		}
	}
	return true
}

func schemaBounds(s map[string]any, minKey, maxKey string, v float64) bool {
	if n, ok := s[minKey].(json.Number); ok {
		f, e := n.Float64()
		if e != nil || v < f {
			return false
		}
	}
	if n, ok := s[maxKey].(json.Number); ok {
		f, e := n.Float64()
		if e != nil || v > f {
			return false
		}
	}
	return true
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var instantPattern = regexp.MustCompile(`^20[0-9]{2}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-](0[0-9]|1[0-9]|2[0-3]):[0-5][0-9])$`)

func UUID(s string) bool { return uuidPattern.MatchString(s) }
func validInstant(s string) bool {
	if !instantPattern.MatchString(s) {
		return false
	}
	_, e := time.Parse(time.RFC3339Nano, s)
	return e == nil
}
func NewUUID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func SHA256(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func Decimal(s string) (uint64, error) {
	n, e := strconv.ParseUint(s, 10, 64)
	if e != nil || n == 0 || strconv.FormatUint(n, 10) != s {
		return 0, ErrProtocol
	}
	return n, nil
}

func OperationalOperationKnown(op string) bool {
	raw, e := protocolFiles.ReadFile("protocol.schema.json")
	if e != nil {
		return false
	}
	root, e := StrictJSON(raw, 64)
	return e == nil && resolveSchema(root, "#/$defs/"+strings.ReplaceAll(op, ".", "_")+"_request") != nil
}
