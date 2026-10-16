package rabbitmq

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const secretAMQP = "amqp://user:s3cr3t-pw@mq:5672/umir"

func TestLoadURLFileChecks(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	load := func(path string) error {
		_, err := LoadURL(envOf(map[string]string{"SVC_AMQP_URL_FILE": path}), "SVC")
		return err
	}
	if err := load(write("ok", secretAMQP+"\n", 0o600)); err != nil {
		t.Fatalf("a private regular file must load: %v", err)
	}
	cases := map[string]string{
		"group readable": write("group", secretAMQP, 0o640),
		"world readable": write("world", secretAMQP, 0o644),
		"oversize":       write("big", secretAMQP+strings.Repeat("x", 5000), 0o600),
		"directory":      dir,
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "ok"), link); err == nil {
		cases["symlink"] = link
	}
	for name, path := range cases {
		err := load(path)
		if err == nil {
			t.Errorf("%s must be rejected", name)
			continue
		}
		if strings.Contains(err.Error(), "s3cr3t-pw") || strings.Contains(err.Error(), dir) {
			t.Errorf("%s: the error shows the secret or the path: %v", name, err)
		}
	}
}
