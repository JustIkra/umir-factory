package faultproxy

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestOperationalStrictSchema(t *testing.T) {
	for _, raw := range []string{`{"version":1,"version":1}`, `{"x":1} {"x":2}`, `{"x":"\xff"}`, strings.Repeat("[", 17) + strings.Repeat("]", 17)} {
		if _, err := StrictJSON([]byte(raw), 16); err == nil {
			t.Fatalf("accepted invalid document")
		}
	}
	data, err := os.ReadFile("protocol-examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var examples struct {
		Positive []struct {
			Schema string
			Value  json.RawMessage
		}
		Negative []struct {
			Schema string
			Value  json.RawMessage
		}
	}
	if err := json.Unmarshal(data, &examples); err != nil {
		t.Fatal(err)
	}
	for i, e := range examples.Positive {
		if err := ValidateOperational(e.Schema, e.Value); err != nil {
			t.Fatalf("positive %d: %v", i, err)
		}
	}
	for i, e := range examples.Negative {
		if err := ValidateOperational(e.Schema, e.Value); err == nil {
			t.Fatalf("negative %d accepted", i)
		}
	}
}
