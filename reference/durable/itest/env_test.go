package itest

import (
	"fmt"
	"strings"
	"testing"
)

type fatalRecorder struct {
	messages []string
}

func (r *fatalRecorder) Helper() {}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}

func TestRequireEnvReportsMissingVariable(t *testing.T) {
	t.Setenv("UMIR_TEST_ITEST_PROBE", "")
	rec := &fatalRecorder{}
	if got := requireEnv(rec, "UMIR_TEST_ITEST_PROBE"); got != "" {
		t.Fatalf("value = %q, want empty", got)
	}
	if len(rec.messages) != 1 {
		t.Fatalf("Fatalf calls = %d, want 1", len(rec.messages))
	}
	msg := rec.messages[0]
	for _, want := range []string{"UMIR_TEST_ITEST_PROBE", "durable-test-infra.sh"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q lacks %q", msg, want)
		}
	}
}

func TestRequireEnvReturnsValueWithoutLeakingIt(t *testing.T) {
	const secret = "synthetic-secret-value"
	t.Setenv("UMIR_TEST_ITEST_PROBE", secret)
	rec := &fatalRecorder{}
	if got := requireEnv(rec, "UMIR_TEST_ITEST_PROBE"); got != secret {
		t.Fatalf("value mismatch")
	}
	if len(rec.messages) != 0 {
		t.Fatalf("unexpected Fatalf: %v", rec.messages)
	}
}

func TestManagementURLFromAMQPURL(t *testing.T) {
	cases := []struct {
		name, amqp, want string
		ok               bool
	}{
		{"local default port", "amqp://u:p%40ss@127.0.0.1:55672/%2F", "http://u:p%40ss@127.0.0.1:55673", true},
		{"other host", "amqp://u:p@10.0.0.5:55672/%2F", "", false},
		{"other port", "amqp://u:p@127.0.0.1:5672/%2F", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := deriveManagementURL(tc.amqp)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("url mismatch")
			}
			if err != nil && strings.Contains(err.Error(), "p@ss") {
				t.Fatalf("error leaks credentials")
			}
		})
	}
}

func TestRandHexIsEightHexCharacters(t *testing.T) {
	seen := map[string]bool{}
	for range 20 {
		s := randHex(t)
		if len(s) != 8 || strings.Trim(s, "0123456789abcdef") != "" {
			t.Fatalf("bad id %q", s)
		}
		seen[s] = true
	}
	if len(seen) < 2 {
		t.Fatalf("ids are not random")
	}
}
