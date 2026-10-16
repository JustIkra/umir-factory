package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// acceptanceDouble records every command and can fail the n-th one (1-based).
type acceptanceDouble struct {
	calls  []string
	failAt int
	envs   [][]string
}

func (d *acceptanceDouble) Run(_ context.Context, _ string, env []string, name string, args ...string) ([]byte, []byte, error) {
	d.calls = append(d.calls, name+" "+strings.Join(args, " "))
	d.envs = append(d.envs, env)
	// The mail preparation creates the inputs file the run patches later.
	if name == "node" && len(args) == 2 && strings.HasSuffix(args[0], "prepare-mail.mjs") {
		if err := os.MkdirAll(args[1], 0o700); err != nil {
			return nil, nil, err
		}
		if err := os.WriteFile(filepath.Join(args[1], "inputs.json"), []byte(`{"initialUser":{"login":"demo"}}`), 0o600); err != nil {
			return nil, nil, err
		}
	}
	if name == "node" && len(args) > 0 && strings.HasSuffix(args[0], "scripts/completion/prepare.mjs") {
		for i := 1; i+1 < len(args); i++ {
			if args[i] == "--directory" {
				if err := os.MkdirAll(args[i+1], 0o700); err != nil {
					return nil, nil, err
				}
				if err := os.WriteFile(filepath.Join(args[i+1], "bundle.json"), []byte("{}\n"), 0o600); err != nil {
					return nil, nil, err
				}
				if err := os.WriteFile(filepath.Join(args[i+1], "compose.env"), []byte("COMPOSE_PROJECT_NAME=umir-completion-ci-abcdef12\n"), 0o600); err != nil {
					return nil, nil, err
				}
				break
			}
		}
	}
	if name == "bash" && len(args) > 0 && strings.HasSuffix(args[0], "scripts/completion/restore.sh") {
		for i := 1; i+1 < len(args); i++ {
			if args[i] == "--bundle-dir" {
				if err := os.MkdirAll(args[i+1], 0o700); err != nil {
					return nil, nil, err
				}
				if err := os.WriteFile(filepath.Join(args[i+1], "compose.env"), []byte("COMPOSE_PROJECT_NAME=umir-completion-ci-abcdef12-restore\n"), 0o600); err != nil {
					return nil, nil, err
				}
				break
			}
		}
	}
	if d.failAt == len(d.calls) {
		return nil, []byte("boom"), errors.New("exit status 1")
	}
	if name == "docker" && len(args) > 1 && args[0] == "volume" {
		return []byte("vol 2026 /mnt\n"), nil, nil
	}
	return nil, nil, nil
}

func testDeps() acceptanceDeps {
	tick := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	return acceptanceDeps{
		random: func() (string, error) { return "abcdef12", nil },
		now: func() time.Time {
			tick = tick.Add(time.Second)
			return tick
		},
	}
}

func runAcceptance(t *testing.T, double *acceptanceDouble, options cliOptions) (int, string, string) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := runCompletionAcceptance(context.Background(), t.TempDir(), options, double, testDeps(), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func indexOfCall(t *testing.T, calls []string, from int, needles ...string) int {
	t.Helper()
	for i := from; i < len(calls); i++ {
		matched := true
		for _, needle := range needles {
			if !strings.Contains(calls[i], needle) {
				matched = false
				break
			}
		}
		if matched {
			return i
		}
	}
	t.Fatalf("no command with %q after #%d in:\n%s", needles, from, strings.Join(calls, "\n"))
	return -1
}

func TestCompletionAcceptanceRunsTheStepsInOrder(t *testing.T) {
	double := &acceptanceDouble{}
	code, stdout, stderr := runAcceptance(t, double, cliOptions{})
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "passed") {
		t.Fatalf("stdout = %q", stdout)
	}
	project := "umir-completion-ci-abcdef12"
	restoreProject := project + "-restore"
	steps := [][]string{
		{"prepare-mail.mjs"},
		{"scripts/completion/prepare.mjs", "--project " + project, "--cookie-mode loopback-development"},
		{"config --quiet"},
		{" pull"},
		{"up -d --wait"},
		{"seed.mjs", "--demo"},
		{"session-check.mjs login"},
		{"transport-health.mjs", "--healthz 8442,8443"},
		{"volume inspect"},
		{"prepare.mjs", "--rotate-leaf"},
		{"up -d --force-recreate private-init"},
		{"restart transport auth"},
		{"volume inspect"},
		{"session-check.mjs verify"},
		{"session-check.mjs signin"},
		{"check-expiry.mjs"},
		{"prepare.mjs", "--reconfigure"},
		{"up -d --force-recreate private-init"},
		{"registration/acceptance.mjs"},
		{"prepare.mjs", "--rollback"},
		{"up -d --force-recreate private-init"},
		{"session-check.mjs signin"},
		{"backup.sh", "--project " + project},
		{"compose -p " + project, " -f compose.completion.yaml", "down --volumes"},
		{"restore.sh", "--project " + restoreProject, "umir-completion-restore-bundle"},
		{"--env-file", "umir-completion-restore-bundle/compose.env", "up -d --wait"},
		{"session-check.mjs signin", "umir-completion-restore-bundle"},
		{"compose -p " + project, " -f compose.completion.yaml", "down --volumes"},
		{"compose -p " + restoreProject, " -f compose.completion.yaml", "down --volumes"},
	}
	at := 0
	for _, needles := range steps {
		at = indexOfCall(t, double.calls, at, needles...) + 1
	}
	if strings.Contains(strings.Join(double.calls, "\n"), "--profile feedback") {
		t.Fatal("the base variant started Feedback")
	}
}

func TestCompletionAcceptanceCleansUpAfterAnErrorAtEveryStep(t *testing.T) {
	full := &acceptanceDouble{}
	if code, _, stderr := runAcceptance(t, full, cliOptions{}); code != 0 {
		t.Fatalf("baseline failed: %s", stderr)
	}
	total := len(full.calls)
	restoreCall := indexOfCall(t, full.calls, 0, "scripts/completion/restore.sh") + 1
	for failAt := 1; failAt < total; failAt++ {
		double := &acceptanceDouble{failAt: failAt}
		dir := t.TempDir()
		code, _, _ := runAcceptance(t, double, cliOptions{evidenceDir: dir})
		if code == 0 {
			t.Fatalf("fail at #%d: success reported", failAt)
		}
		last := double.calls[len(double.calls)-1]
		wantProject := "docker compose -p umir-completion-ci-abcdef12-restore"
		if failAt < restoreCall {
			wantProject = "docker compose -p umir-completion-ci-abcdef12"
		}
		if failAt == 1 {
			if strings.Contains(strings.Join(double.calls, "\n"), " down --volumes") {
				t.Fatalf("fail at #%d: cleanup ran before a Compose bundle existed: %v", failAt, double.calls)
			}
		} else if !strings.HasPrefix(last, wantProject) || !strings.Contains(last, "down --volumes --remove-orphans") {
			// An error in the cleanup command itself is the last command by definition.
			t.Fatalf("fail at #%d: last command %q, want destination-project cleanup", failAt, last)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "completion-acceptance-base.json"))
		if err != nil {
			t.Fatalf("fail at #%d: no evidence: %v", failAt, err)
		}
		var evidence acceptanceEvidence
		if err := json.Unmarshal(raw, &evidence); err != nil || evidence.Passed {
			t.Fatalf("fail at #%d: evidence = %s", failAt, raw)
		}
	}
}

func TestCompletionAcceptanceRemovesVolumesOnlyOfItsOwnPrefix(t *testing.T) {
	for _, project := range []string{"", "umir-completion-", "umir-completion-ci-", "umir-prod", "umir-completion-test-x", "other-ci-1"} {
		double := &acceptanceDouble{}
		run := &acceptanceRun{ctx: context.Background(), executor: double, project: project, factoryDir: t.TempDir()}
		if err := run.down(); err == nil {
			t.Fatalf("project %q: down was allowed", project)
		}
		if len(double.calls) != 0 {
			t.Fatalf("project %q: commands ran: %v", project, double.calls)
		}
	}
	double := &acceptanceDouble{}
	bundle := t.TempDir()
	restored := t.TempDir()
	if err := os.WriteFile(filepath.Join(bundle, "compose.env"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(restored, "compose.env"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	run := &acceptanceRun{ctx: context.Background(), executor: double, project: "umir-completion-ci-1a2b3c", restoreProject: "umir-completion-ci-1a2b3c-restore", bundle: bundle, restoredBundle: restored, factoryDir: t.TempDir()}
	if err := run.down(); err != nil {
		t.Fatal(err)
	}
	if len(double.calls) != 2 || !strings.Contains(double.calls[0], "-p umir-completion-ci-1a2b3c --env-file") || !strings.Contains(double.calls[1], "-p umir-completion-ci-1a2b3c-restore --env-file") {
		t.Fatalf("calls = %v", double.calls)
	}
	if err := run.downProject("umir-completion-ci-1a2b3c-restore"); err != nil {
		t.Fatal(err)
	}
	if len(double.calls) != 3 || !strings.Contains(double.calls[2], "-p umir-completion-ci-1a2b3c-restore --env-file") {
		t.Fatalf("destination cleanup calls = %v", double.calls)
	}
}

func TestCompletionAcceptanceEvidenceHoldsNoSecrets(t *testing.T) {
	double := &acceptanceDouble{}
	dir := t.TempDir()
	if code, _, stderr := runAcceptance(t, double, cliOptions{evidenceDir: dir}); code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "completion-acceptance-base.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence acceptanceEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	if !evidence.Passed || evidence.Variant != "base" || evidence.Project != "umir-completion-ci-abcdef12" || evidence.RestoreProject != "umir-completion-ci-abcdef12-restore" || !evidence.SourceBundleUnchanged || evidence.Cleanup != "removed" {
		t.Fatalf("evidence = %s", raw)
	}
	names := map[string]bool{}
	for _, step := range evidence.Steps {
		names[step.Name] = step.OK
	}
	for _, want := range []string{"prepare", "up", "seed", "transport-healthz", "rotate-leaf", "reconfigure", "rollback", "backup", "destroy", "restore"} {
		if !names[want] {
			t.Fatalf("step %s missing or failed in %s", want, raw)
		}
	}
	if evidence.RestoreSeconds <= 0 {
		t.Fatal("the restore time is not recorded")
	}
	for _, secret := range []string{"password", "backup.key", "secret", "token", "BEGIN"} {
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(secret)) {
			t.Fatalf("evidence mentions %q: %s", secret, raw)
		}
	}
}

func TestCompletionAcceptanceFeedbackVariant(t *testing.T) {
	double := &acceptanceDouble{}
	dir := t.TempDir()
	source := t.TempDir()
	code, _, stderr := runAcceptance(t, double, cliOptions{feedback: true, feedbackSource: source, evidenceDir: dir})
	if code != 0 {
		t.Fatalf("code %d: %s", code, stderr)
	}
	joined := strings.Join(double.calls, "\n")
	if !strings.Contains(joined, "docker build -t umir-completion-ci-feedback:abcdef12 "+source) {
		t.Fatalf("no image build in:\n%s", joined)
	}
	if !strings.Contains(joined, "--profile feedback") || !strings.Contains(joined, "8445 --ca /run/auth-health/ca.crt --healthz 8442,8443,8445") {
		t.Fatalf("Feedback is not started and checked:\n%s", joined)
	}
	if _, err := os.Stat(filepath.Join(dir, "completion-acceptance-feedback.json")); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, env := range double.envs {
		for _, entry := range env {
			if strings.HasPrefix(entry, "COMPLETION_FEEDBACK_IMAGE=umir-completion-ci-feedback:") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the built Feedback image is not passed to Compose")
	}
}

func TestCompletionAcceptanceOptions(t *testing.T) {
	for _, args := range [][]string{
		{"--feedback"},
		{"--feedback-source", "/abs"},
		{"--feedback", "--feedback-source", "relative"},
		{"--evidence-dir", "relative"},
	} {
		if _, err := parseOptions("completion-acceptance", append([]string{"--factory-dir", "/abs"}, args...)); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
	options, err := parseOptions("completion-acceptance", []string{"--factory-dir", "/abs", "--feedback", "--feedback-source", "/src", "--evidence-dir", "/ev"})
	if err != nil || !options.feedback || options.feedbackSource != "/src" || options.evidenceDir != "/ev" {
		t.Fatalf("options = %#v, %v", options, err)
	}
}
