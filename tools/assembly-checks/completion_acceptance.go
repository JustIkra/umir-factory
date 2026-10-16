package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	acceptanceProjectPrefix = "umir-completion-ci-"
	acceptanceWebPort       = "18580"
	acceptanceMailPort      = "19125"
	acceptanceOrigin        = "http://127.0.0.1:" + acceptanceWebPort
	acceptanceMailURL       = "http://127.0.0.1:" + acceptanceMailPort
	composeFile             = "compose.completion.yaml"
	composeMailFile         = "tests/examples/completion/compose.mail.yaml"
	healthzPorts            = "8442,8443"
)

// acceptanceDeps are the parts of the run that tests replace.
type acceptanceDeps struct {
	random func() (string, error)
	now    func() time.Time
}

func defaultAcceptanceDeps() acceptanceDeps {
	return acceptanceDeps{
		random: func() (string, error) {
			buffer := make([]byte, 4)
			if _, err := rand.Read(buffer); err != nil {
				return "", err
			}
			return hex.EncodeToString(buffer), nil
		},
		now: time.Now,
	}
}

type acceptanceStep struct {
	Name    string  `json:"name"`
	OK      bool    `json:"ok"`
	Seconds float64 `json:"seconds"`
	Detail  string  `json:"detail,omitempty"`
}

type acceptanceEvidence struct {
	Variant        string           `json:"variant"`
	Project        string           `json:"project"`
	StartedAt      string           `json:"startedAt"`
	FinishedAt     string           `json:"finishedAt"`
	Passed         bool             `json:"passed"`
	RestoreSeconds float64          `json:"restoreSeconds,omitempty"`
	Volumes        map[string]any   `json:"volumes,omitempty"`
	Steps          []acceptanceStep `json:"steps"`
	Cleanup        string           `json:"cleanup"`
}

type acceptanceRun struct {
	ctx         context.Context
	executor    commandExecutor
	deps        acceptanceDeps
	factoryDir  string
	feedback    bool
	feedbackSrc string

	project  string
	tmp      string
	bundle   string
	mail     string
	keyFile  string
	session  string
	seed     string
	data     string
	keys     string
	extraEnv []string
	evidence acceptanceEvidence
	failed   bool
}

func (r *acceptanceRun) env() []string {
	return commandEnv(append([]string{"COMPLETION_TEST_MAIL_DIR=" + r.mail}, r.extraEnv...)...)
}

// run starts one command in the factory checkout.
func (r *acceptanceRun) run(name string, args ...string) (string, error) {
	out, errOut, err := r.executor.Run(r.ctx, r.factoryDir, r.env(), name, args...)
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %s: %w", name, firstArgs(args), tail(string(errOut)), err)
	}
	return string(out), nil
}

func firstArgs(args []string) string {
	if len(args) > 3 {
		args = args[:3]
	}
	return strings.Join(args, " ")
}

func tail(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 300 {
		return "..." + text[len(text)-300:]
	}
	return text
}

func (r *acceptanceRun) node(script string, args ...string) (string, error) {
	return r.run("node", append([]string{script}, args...)...)
}

// compose runs Docker Compose for the stand with the bundle environment.
func (r *acceptanceRun) compose(args ...string) (string, error) {
	base := []string{"compose", "--env-file", filepath.Join(r.bundle, "compose.env"), "-f", composeFile, "-f", composeMailFile}
	if r.feedback {
		base = append(base, "--profile", "feedback")
	}
	return r.run("docker", append(base, args...)...)
}

func (r *acceptanceRun) prepare(extra ...string) (string, error) {
	args := []string{"--directory", r.bundle, "--inputs", filepath.Join(r.mail, "inputs.json"), "--project", r.project, "--origin", acceptanceOrigin, "--cookie-mode", "loopback-development"}
	return r.node("scripts/completion/prepare.mjs", append(args, extra...)...)
}

// applyGeneration puts a prepared generation into the running stand: the steps of
// the README, without removing any volume.
func (r *acceptanceRun) applyGeneration() error {
	if _, err := r.compose("up", "-d", "--force-recreate", "private-init"); err != nil {
		return err
	}
	if _, err := r.compose("restart", "transport", "auth"); err != nil {
		return err
	}
	_, err := r.compose("up", "-d", "--wait", "--wait-timeout", "240")
	return err
}

func (r *acceptanceRun) volumeIdentity() (string, error) {
	return r.run("docker", "volume", "inspect", "-f", "{{.Name}} {{.CreatedAt}} {{.Mountpoint}}", r.project+"_auth-state", r.project+"_auth-config")
}

func (r *acceptanceRun) sessionCheck(args ...string) error {
	_, err := r.node("tests/examples/completion/session-check.mjs", args...)
	return err
}

// patchInputs turns public registration on in the private inputs (a reviewed
// reconfiguration, which must not touch the keys or the first user).
func (r *acceptanceRun) patchInputs() error {
	path := filepath.Join(r.mail, "inputs.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read inputs: %w", err)
	}
	var inputs map[string]any
	if err := json.Unmarshal(data, &inputs); err != nil {
		return fmt.Errorf("parse inputs: %w", err)
	}
	inputs["registration"] = map[string]any{"enabled": true, "mailLimitPer10Minutes": 60, "organizationsPerUser": 10, "organizationCreationsPerHour": 10, "wrongCodesPerAddressPerHour": 20}
	out, err := json.MarshalIndent(inputs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

type namedStep struct {
	name string
	run  func() error
}

func (r *acceptanceRun) steps() []namedStep {
	var before string
	var restoreStart time.Time
	script := []namedStep{
		{"prepare-mail", func() error {
			_, err := r.node("tests/examples/completion/prepare-mail.mjs", r.mail)
			return err
		}},
		{"prepare", func() error { _, err := r.prepare(); return err }},
	}
	if r.feedback {
		script = append(script, namedStep{"build-feedback", func() error {
			image := "umir-completion-ci-feedback:" + strings.TrimPrefix(r.project, acceptanceProjectPrefix)
			if _, err := r.run("docker", "build", "-t", image, r.feedbackSrc); err != nil {
				return err
			}
			envFile := filepath.Join(r.tmp, "feedback-providers.env")
			const providers = "FEEDBACK_SMTP_HOST=127.0.0.1\nFEEDBACK_SMTP_PORT=25\nFEEDBACK_SMTP_TLS_MODE=starttls\nFEEDBACK_SMTP_USERNAME=acceptance-dummy\nFEEDBACK_SMTP_PASSWORD=acceptance-dummy\nFEEDBACK_MAIL_FROM=feedback@invalid.example\nFEEDBACK_MAIL_TO=operator@invalid.example\nFEEDBACK_TELEGRAM_BOT_TOKEN=123456789:acceptance-dummy\nFEEDBACK_TELEGRAM_CHAT_ID=-1000000000001\nFEEDBACK_MAX_STORE_BYTES=1048576\nFEEDBACK_RETENTION_DAYS=30\nFEEDBACK_CONSENT_VERSION=v1\nFEEDBACK_TELEGRAM_PROXY_USERNAME=acceptance-dummy\nFEEDBACK_TELEGRAM_PROXY_PASSWORD=acceptance-dummy\nFEEDBACK_TELEGRAM_PROXY_URL=http://telegram-proxy.invalid:3128\n"
			if err := os.WriteFile(envFile, []byte(providers), 0o600); err != nil {
				return err
			}
			r.extraEnv = append(r.extraEnv, "COMPLETION_FEEDBACK_IMAGE="+image, "COMPLETION_FEEDBACK_ENV_FILE="+envFile)
			return nil
		}})
	}
	script = append(script,
		namedStep{"compose-config", func() error { _, err := r.compose("config", "--quiet"); return err }},
		namedStep{"pull", func() error {
			args := []string{"pull"}
			if r.feedback {
				args = append(args, "--ignore-buildable", "--ignore-pull-failures")
			}
			_, err := r.compose(args...)
			return err
		}},
		namedStep{"up", func() error {
			_, err := r.compose("up", "-d", "--wait", "--wait-timeout", "240")
			return err
		}},
		namedStep{"seed", func() error {
			_, err := r.node("tests/examples/completion/seed.mjs", "--directory", r.bundle, "--state-file", r.seed, "--demo")
			return err
		}},
		namedStep{"session-login", func() error {
			return r.sessionCheck("login", "--directory", r.bundle, "--session-file", r.session)
		}},
		namedStep{"transport-healthz", func() error {
			ports := healthzPorts
			all := "8442 8443 8444"
			if r.feedback {
				ports += ",8445"
				all += " 8445"
			}
			args := []string{"exec", "-T", "transport-ready", "node", "/transport-health.mjs", "transport"}
			args = append(args, strings.Fields(all)...)
			args = append(args, "--ca", "/run/auth-health/ca.crt", "--healthz", ports)
			_, err := r.compose(args...)
			return err
		}},
		namedStep{"rotate-leaf", func() error {
			var err error
			if before, err = r.volumeIdentity(); err != nil {
				return err
			}
			if _, err := r.prepare("--rotate-leaf"); err != nil {
				return err
			}
			if err := r.applyGeneration(); err != nil {
				return err
			}
			after, err := r.volumeIdentity()
			if err != nil {
				return err
			}
			if after != before {
				return errors.New("a rotation changed the auth-state or auth-config volume")
			}
			r.evidence.Volumes = map[string]any{"unchangedAcrossRotation": true}
			if err := r.sessionCheck("verify", "--session-file", r.session); err != nil {
				return fmt.Errorf("the earlier session stopped working: %w", err)
			}
			if err := r.sessionCheck("signin", "--directory", r.bundle); err != nil {
				return err
			}
			// The bundle carries the new leaf: more than 300 days are left on it.
			_, err = r.node("scripts/completion/check-expiry.mjs", "--directory", r.bundle, "--warn-days", "300")
			return err
		}},
		namedStep{"reconfigure", func() error {
			if err := r.patchInputs(); err != nil {
				return err
			}
			if _, err := r.prepare("--reconfigure"); err != nil {
				return err
			}
			if err := r.applyGeneration(); err != nil {
				return err
			}
			if _, err := r.node("tests/examples/registration/acceptance.mjs", "--origin", acceptanceOrigin, "--mailpit", acceptanceMailURL, "--output", filepath.Join(r.tmp, "registration.json")); err != nil {
				return fmt.Errorf("registration after reconfigure: %w", err)
			}
			return r.sessionCheck("verify", "--session-file", r.session)
		}},
		namedStep{"rollback", func() error {
			if _, err := r.prepare("--rollback"); err != nil {
				return err
			}
			if err := r.applyGeneration(); err != nil {
				return err
			}
			return r.sessionCheck("signin", "--directory", r.bundle)
		}},
		namedStep{"backup", func() error {
			_, err := r.run("bash", "scripts/completion/backup.sh", "--project", r.project, "--bundle", r.bundle, "--key-file", r.keyFile, "--out", r.data, "--keys-out", r.keys)
			return err
		}},
		namedStep{"destroy", func() error {
			if err := r.down(); err != nil {
				return err
			}
			return os.RemoveAll(r.bundle)
		}},
		namedStep{"restore", func() error {
			restoreStart = r.deps.now()
			if _, err := r.run("bash", "scripts/completion/restore.sh", "--project", r.project, "--bundle-dir", r.bundle, "--from", r.data, "--keys-from", r.keys, "--key-file", r.keyFile); err != nil {
				return err
			}
			if _, err := r.compose("up", "-d", "--wait", "--wait-timeout", "240"); err != nil {
				return err
			}
			if err := r.sessionCheck("signin", "--directory", r.bundle); err != nil {
				return fmt.Errorf("sign-in after restore: %w", err)
			}
			r.evidence.RestoreSeconds = r.deps.now().Sub(restoreStart).Seconds()
			return nil
		}},
	)
	return script
}

// down removes the containers, networks and, for a project this run owns, the volumes.
func (r *acceptanceRun) down() error {
	if !strings.HasPrefix(r.project, acceptanceProjectPrefix) || len(r.project) <= len(acceptanceProjectPrefix) {
		return fmt.Errorf("refusing to remove volumes of project %q: only %s* is owned by this command", r.project, acceptanceProjectPrefix)
	}
	_, err := r.run("docker", "compose", "-p", r.project, "down", "--volumes", "--remove-orphans")
	return err
}

func (r *acceptanceRun) execute() {
	for _, step := range r.steps() {
		started := r.deps.now()
		err := step.run()
		entry := acceptanceStep{Name: step.name, OK: err == nil, Seconds: r.deps.now().Sub(started).Seconds()}
		if err != nil {
			entry.Detail = tail(err.Error())
			r.failed = true
		}
		r.evidence.Steps = append(r.evidence.Steps, entry)
		if err != nil {
			return
		}
	}
}

func variantName(feedback bool) string {
	if feedback {
		return "feedback"
	}
	return "base"
}

func runCompletionAcceptance(ctx context.Context, factoryDir string, options cliOptions, executor commandExecutor, deps acceptanceDeps, stdout, stderr io.Writer) int {
	suffix, err := deps.random()
	if err != nil || len(suffix) < 6 {
		writeFailure(stderr, "cannot generate a project name\n")
		return 1
	}
	tmp, err := os.MkdirTemp("", acceptanceProjectPrefix)
	if err != nil {
		writeFailure(stderr, "create temporary directory: %v\n", err)
		return 1
	}
	// The private inputs must be given by real paths (no symlinks, as on macOS /var).
	if resolved, resolveErr := filepath.EvalSymlinks(tmp); resolveErr == nil {
		tmp = resolved
	}
	run := &acceptanceRun{
		ctx: ctx, executor: executor, deps: deps, factoryDir: factoryDir,
		feedback: options.feedback, feedbackSrc: options.feedbackSource,
		project: acceptanceProjectPrefix + suffix, tmp: tmp,
	}
	run.bundle = filepath.Join(tmp, "umir-completion-bundle")
	run.mail = filepath.Join(tmp, "umir-completion-mail")
	run.session = filepath.Join(tmp, "session.json")
	run.seed = filepath.Join(tmp, "seed.json")
	run.data = filepath.Join(tmp, "backup-data")
	run.keys = filepath.Join(tmp, "backup-keys")
	run.keyFile = filepath.Join(tmp, "backup.key")
	run.evidence = acceptanceEvidence{Variant: variantName(options.feedback), Project: run.project, StartedAt: deps.now().UTC().Format(time.RFC3339)}

	// The temporary directory and the stand are removed on every exit path.
	defer func() {
		if err := os.RemoveAll(tmp); err != nil {
			writeFailure(stderr, "remove temporary directory: %v\n", err)
		}
	}()
	keyBytes := make([]byte, 24)
	if _, err := rand.Read(keyBytes); err != nil {
		writeFailure(stderr, "generate backup key: %v\n", err)
		return 1
	}
	if err := os.WriteFile(run.keyFile, []byte(hex.EncodeToString(keyBytes)+hex.EncodeToString(keyBytes)), 0o600); err != nil {
		writeFailure(stderr, "write backup key: %v\n", err)
		return 1
	}

	run.execute()
	run.evidence.Passed = !run.failed
	run.evidence.Cleanup = "removed"
	if err := run.down(); err != nil {
		run.evidence.Cleanup = "failed: " + tail(err.Error())
		run.failed = true
		run.evidence.Passed = false
	}
	run.evidence.FinishedAt = deps.now().UTC().Format(time.RFC3339)

	if options.evidenceDir != "" {
		if err := writeAcceptanceEvidence(options.evidenceDir, run.evidence); err != nil {
			writeFailure(stderr, "%v\n", err)
			return 1
		}
	}
	if run.failed {
		for _, step := range run.evidence.Steps {
			if !step.OK {
				writeFailure(stderr, "completion acceptance failed at %s: %s\n", step.Name, step.Detail)
			}
		}
		if run.evidence.Cleanup != "removed" {
			writeFailure(stderr, "completion acceptance cleanup: %s\n", run.evidence.Cleanup)
		}
		return 1
	}
	if err := writeMessage(stdout, "Completion acceptance passed (%s, %d steps, restore %.0f s)\n", run.evidence.Variant, len(run.evidence.Steps), run.evidence.RestoreSeconds); err != nil {
		return 1
	}
	return 0
}

func writeAcceptanceEvidence(directory string, evidence acceptanceEvidence) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create evidence directory: %w", err)
	}
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "completion-acceptance-"+evidence.Variant+".json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write evidence: %w", err)
	}
	return nil
}
