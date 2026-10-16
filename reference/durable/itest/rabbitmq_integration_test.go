//go:build integration

package itest

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

var vhostPattern = regexp.MustCompile(`^t_factory_[0-9a-f]{8}$`)

func TestNewVHostCreatesAndDeletes(t *testing.T) {
	var name string
	m := newManagement(t)
	t.Run("use", func(t *testing.T) {
		raw := NewVHost(t)
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse vhost URL")
		}
		name = strings.TrimPrefix(u.Path, "/")
		if !vhostPattern.MatchString(name) {
			t.Fatalf("virtual host %q does not match %s", name, vhostPattern)
		}
		ok, err := vhostExists(t.Context(), m, name)
		if err != nil || !ok {
			t.Fatalf("virtual host %s must exist: ok=%v err=%v", name, ok, err)
		}
		conn, err := amqp.Dial(raw)
		if err != nil {
			t.Fatalf("dial the new virtual host: %v", err)
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		}()
		ch, err := conn.Channel()
		if err != nil {
			t.Fatalf("channel: %v", err)
		}
		if _, err := ch.QueueDeclare("probe", true, false, false, false, nil); err != nil {
			t.Fatalf("declare in the new virtual host (permissions): %v", err)
		}
	})
	if name == "" {
		t.Fatal("subtest did not report the virtual host")
	}
	ok, err := vhostExists(context.Background(), m, name)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if ok {
		t.Fatalf("virtual host %s still exists after cleanup", name)
	}
}

func TestManagementRefusesForeignVHosts(t *testing.T) {
	m := newManagement(t)
	if err := m.deleteVHost(context.Background(), "/"); err == nil {
		t.Fatal("deleteVHost accepted the default virtual host")
	}
	if err := m.createVHost(context.Background(), "other", "guest"); err == nil {
		t.Fatal("createVHost accepted a foreign name")
	}
}

// vhostExists reports whether the management API knows the virtual host.
func vhostExists(ctx context.Context, m *management, name string) (bool, error) {
	status, err := m.do(ctx, http.MethodGet, []string{"api", "vhosts", name}, nil)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("check virtual host %s: management answered %d", name, status)
	}
}
