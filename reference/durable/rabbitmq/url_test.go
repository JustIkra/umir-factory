package rabbitmq

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadURLFromVariable(t *testing.T) {
	got, err := LoadURL(envOf(map[string]string{"GATEWAY_AMQP_URL": "amqp://u:p@mq:5672/umir"}), "GATEWAY")
	if err != nil {
		t.Fatalf("LoadURL: %v", err)
	}
	if got != "amqp://u:p@mq:5672/umir" {
		t.Fatalf("unexpected value")
	}
}

func TestLoadURLFromFileTrimsNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amqp")
	if err := os.WriteFile(path, []byte("amqp://u:p@mq:5672/umir\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadURL(envOf(map[string]string{"AUTH_AMQP_URL_FILE": path}), "AUTH")
	if err != nil {
		t.Fatalf("LoadURL: %v", err)
	}
	if got != "amqp://u:p@mq:5672/umir" {
		t.Fatalf("unexpected value from file")
	}
}

func TestLoadURLPrefixWithTrailingUnderscore(t *testing.T) {
	if _, err := LoadURL(envOf(map[string]string{"X_AMQP_URL": "amqp://mq/"}), "X_"); err != nil {
		t.Fatalf("LoadURL: %v", err)
	}
}

func TestLoadURLRejectsBothAndNeither(t *testing.T) {
	both := map[string]string{"S_AMQP_URL": "amqp://u:secretpw@mq/", "S_AMQP_URL_FILE": "/run/secrets/x"}
	if _, err := LoadURL(envOf(both), "S"); err == nil {
		t.Fatal("both variables set must be an error")
	} else if strings.Contains(err.Error(), "secretpw") {
		t.Fatalf("error leaks the value: %v", err)
	}
	if _, err := LoadURL(envOf(nil), "S"); err == nil {
		t.Fatal("neither variable set must be an error")
	}
}

func TestLoadURLErrorsNeverContainValue(t *testing.T) {
	cases := map[string]map[string]string{
		"bad scheme":  {"S_AMQP_URL": "http://u:secretpw@mq/"},
		"unparseable": {"S_AMQP_URL": "amqp://u:secretpw@mq:notaport/"},
		"empty file":  {},
	}
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases["empty file"]["S_AMQP_URL_FILE"] = empty
	for name, env := range cases {
		_, err := LoadURL(envOf(env), "S")
		if err == nil {
			t.Fatalf("%s: want an error", name)
		}
		if strings.Contains(err.Error(), "secretpw") {
			t.Fatalf("%s: error leaks the value: %v", name, err)
		}
	}
}

func TestLoadURLMissingFileNamesVariable(t *testing.T) {
	_, err := LoadURL(envOf(map[string]string{"S_AMQP_URL_FILE": filepath.Join(t.TempDir(), "absent")}), "S")
	if err == nil || !strings.Contains(err.Error(), "S_AMQP_URL_FILE") {
		t.Fatalf("want an error naming the variable, got %v", err)
	}
}
