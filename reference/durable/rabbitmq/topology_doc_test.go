package rabbitmq

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// This test reads docs/durable/events.md of the factory repository. It is a
// factory-only check and is not copied into services.

const eventsDoc = "../../../docs/durable/events.md"

var (
	tableRow    = regexp.MustCompile("^\\|\\s*`([^`]+)`\\s*\\|\\s*`([^`]+)`\\s*\\|")
	exchangeRow = regexp.MustCompile("^\\|\\s*`([^`]+)`\\s*\\|\\s*(topic|direct|fanout|headers)\\s*\\|")
)

func TestDefaultTopologyMatchesEventsDocument(t *testing.T) {
	raw, err := os.ReadFile(eventsDoc)
	if err != nil {
		t.Fatalf("read %s: %v", eventsDoc, err)
	}
	topo := DefaultTopology(TopologyOptions{Feedback: true})
	exchangeKind := map[string]string{}
	for _, e := range topo.Exchanges {
		exchangeKind[e.Name] = e.Kind
	}

	section := ""
	var docExchanges, authKeys, warehouseKeys, taskKeys []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "## ") {
			section = line
			continue
		}
		if em := exchangeRow.FindStringSubmatch(line); em != nil && strings.HasPrefix(section, "## Обменники") {
			docExchanges = append(docExchanges, em[1])
			if exchangeKind[em[1]] != em[2] {
				t.Errorf("exchange %s: document says %q, topology has %q", em[1], em[2], exchangeKind[em[1]])
			}
			continue
		}
		m := tableRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch {
		case strings.HasPrefix(section, "## Ключи маршрутизации"):
			switch m[1] {
			case "umir.auth":
				authKeys = append(authKeys, m[2])
			case "umir.events":
				warehouseKeys = append(warehouseKeys, m[2])
			case "umir.tasks":
				taskKeys = append(taskKeys, m[2])
			default:
				t.Errorf("unknown exchange %q in the routing table", m[1])
			}
		}
	}

	slices.Sort(docExchanges)
	var have []string
	for name := range exchangeKind {
		have = append(have, name)
	}
	slices.Sort(have)
	if !slices.Equal(docExchanges, have) {
		t.Errorf("exchanges: document %v, topology %v", docExchanges, have)
	}
	if len(authKeys) == 0 || len(warehouseKeys) == 0 || len(taskKeys) == 0 {
		t.Fatalf("routing table not parsed: auth=%d events=%d tasks=%d", len(authKeys), len(warehouseKeys), len(taskKeys))
	}
	if !slices.Equal(authKeys, AuthEventKeys) {
		t.Errorf("auth keys: document %v, AuthEventKeys %v", authKeys, AuthEventKeys)
	}
	if !slices.Equal(warehouseKeys, WarehouseEventKeys) {
		t.Errorf("warehouse keys: document %v, WarehouseEventKeys %v", warehouseKeys, WarehouseEventKeys)
	}
	for _, key := range taskKeys {
		if !hasBinding(topo, key, "umir.tasks", key) {
			t.Errorf("task key %s is not bound to its queue", key)
		}
	}
	// Consumers of the fan-out keys: Gateway binds all seven auth keys, Serving five.
	gAuth, err := GatewayAuthQueue("x")
	if err != nil {
		t.Fatal(err)
	}
	var gKeys []string
	for _, b := range gAuth.Bindings {
		gKeys = append(gKeys, b.Key)
	}
	if !slices.Equal(gKeys, authKeys) {
		t.Errorf("gateway.auth keys %v differ from the document %v", gKeys, authKeys)
	}
	checkQueueArguments(t, string(raw), topo)
	// Every queue of the "Очереди" section that is declared by name in the document exists.
	for _, name := range []string{"auth.mail", "feedback.smtp", "feedback.telegram", "feedback.auth"} {
		for _, suffix := range []string{"", ".retry", ".dlq"} {
			if !strings.Contains(string(raw), "`"+name+suffix+"`") && suffix == "" {
				t.Errorf("document does not mention %s", name)
			}
			queueByName(t, topo, name+suffix)
		}
	}
}

var (
	queueRow = regexp.MustCompile("^\\|\\s*`<q>(\\.retry|\\.dlq)?`\\s*\\|([^|]*)\\|")
	argToken = regexp.MustCompile("x-[a-z-]+=[^;`\\s]+")
)

// checkQueueArguments compares the argument column of the work-queue table
// (<q>, <q>.retry, <q>.dlq) with the arguments the topology declares for
// auth.mail.
func checkQueueArguments(t *testing.T, doc string, topo Topology) {
	t.Helper()
	found := map[string][]string{}
	for _, line := range strings.Split(doc, "\n") {
		m := queueRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var tokens []string
		for _, tok := range argToken.FindAllString(m[2], -1) {
			tokens = append(tokens, strings.ReplaceAll(tok, "<q>", "auth.mail"))
		}
		slices.Sort(tokens)
		found[m[1]] = tokens
	}
	for _, suffix := range []string{"", ".retry", ".dlq"} {
		doc, ok := found[suffix]
		if !ok {
			t.Errorf("queue table has no row for <q>%s", suffix)
			continue
		}
		var have []string
		for k, v := range queueByName(t, topo, "auth.mail"+suffix).Args {
			have = append(have, fmt.Sprintf("%s=%v", k, v))
		}
		slices.Sort(have)
		if !slices.Equal(doc, have) {
			t.Errorf("queue <q>%s: document arguments %v, topology %v", suffix, doc, have)
		}
	}
}
