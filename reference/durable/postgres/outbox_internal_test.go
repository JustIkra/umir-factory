package postgres

import (
	"maps"
	"testing"
)

func TestDecodeHeadersConvertsEveryJSONValueToAString(t *testing.T) {
	raw := []byte(`{"s":"text","n":42,"f":1.50,"big":12345678901234567890,"t":true,"z":null,"o":{"a":[1,2]},"l":["x",2]}`)
	got, err := decodeHeaders(raw)
	if err != nil {
		t.Fatalf("decodeHeaders: %v", err)
	}
	want := map[string]string{
		"s": "text", "n": "42", "f": "1.50", "big": "12345678901234567890", "t": "true",
		"o": `{"a":[1,2]}`, "l": `["x",2]`,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("headers = %v, want %v", got, want)
	}
}

func TestDecodeHeadersEmptyAndInvalid(t *testing.T) {
	got, err := decodeHeaders([]byte(`{}`))
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty object: %v %v", got, err)
	}
	for _, raw := range []string{`[]`, `"x"`, `7`, `null`, ``} {
		if _, err := decodeHeaders([]byte(raw)); err == nil {
			t.Errorf("%q must be rejected", raw)
		}
	}
}
