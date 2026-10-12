package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFeedbackAcceptanceRequiresCandidateAuthAndExactSHA(t *testing.T) {
	factory := t.TempDir()
	args := append(candidateArgs(factory), "--auth", "--feedback-ref", strings.Repeat("e", 40))
	options, err := parseOptions("http-acceptance", args)
	if err != nil || options.feedbackRef != strings.Repeat("e", 40) {
		t.Fatalf("candidate feedback options = %+v, %v", options, err)
	}

	for name, invalid := range map[string][]string{
		"without auth": append(candidateArgs(factory), "--feedback-ref", strings.Repeat("e", 40)),
		"bad sha":      append(append(candidateArgs(factory), "--auth", "--feedback-ref"), strings.Repeat("E", 40)),
		"selected":     {"--factory-dir", factory, "--selected", "--feedback-ref", strings.Repeat("e", 40)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseOptions("http-acceptance", invalid); err == nil {
				t.Fatal("invalid feedback acceptance options were accepted")
			}
		})
	}
	if _, err := parseOptions("http-acceptance", candidateArgs(factory)); err != nil {
		t.Fatalf("legacy candidate path changed: %v", err)
	}
}

func TestFeedbackMarkerMustBeExplicitOnceAtTopLevel(t *testing.T) {
	for name, content := range map[string]string{
		"exact":     "# comment\nx-umir-feedback-acceptance: candidate\nservices: {}\n",
		"selected":  "x-umir-feedback-acceptance: selected\n",
		"duplicate": "x-umir-feedback-acceptance: candidate\nx-umir-feedback-acceptance: candidate\n",
		"absent":    "services: {}\n",
		"indented":  "services:\n  x-umir-feedback-acceptance: candidate\n",
	} {
		t.Run(name, func(t *testing.T) {
			factory := t.TempDir()
			if err := os.WriteFile(filepath.Join(factory, feedbackOverrideFile), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			err := verifyFeedbackMarker(factory)
			if (name == "exact") != (err == nil) {
				t.Fatalf("verifyFeedbackMarker = %v", err)
			}
		})
	}
	if err := verifyFeedbackMarker(t.TempDir()); err == nil {
		t.Fatal("a missing compose.feedback.yaml was accepted")
	}
}

func TestFeedbackMatrixRequiresF01ThroughF05OnlyWhenEnabled(t *testing.T) {
	outcomes := append(completeOutcomes(true), "F01:passed", "F02:passed", "F03:passed", "F04:passed", "F05:passed")
	if err := validateHTTPOutcomes(outcomes, true, true, false); err != nil {
		t.Fatalf("complete feedback matrix rejected: %v", err)
	}
	withoutF03 := slices.DeleteFunc(slices.Clone(outcomes), func(outcome string) bool { return outcome == "F03:passed" })
	if err := validateHTTPOutcomes(withoutF03, true, true, false); err == nil {
		t.Fatal("feedback matrix accepted a missing F03")
	}
	if err := validateHTTPOutcomes(outcomes, true, false, false); err == nil {
		t.Fatal("legacy auth matrix accepted feedback outcomes")
	}
}

func TestFeedbackComposeInputsContainOnlySyntheticClosedConfiguration(t *testing.T) {
	feedback, err := writeFeedbackInputs(t.TempDir(), strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(feedback.envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"FEEDBACK_SMTP_HOST=127.0.0.1",
		"FEEDBACK_SMTP_PORT=25",
		"FEEDBACK_SMTP_TLS_MODE=starttls",
		"FEEDBACK_SMTP_USERNAME=acceptance-dummy",
		"FEEDBACK_SMTP_PASSWORD=acceptance-dummy",
		"FEEDBACK_MAIL_FROM=feedback@invalid.example",
		"FEEDBACK_MAIL_TO=operator@invalid.example",
		"FEEDBACK_TELEGRAM_BOT_TOKEN=123456789:acceptance-dummy",
		"FEEDBACK_TELEGRAM_CHAT_ID=-1000000000001",
		"FEEDBACK_MAX_STORE_BYTES=1048576",
		"FEEDBACK_RETENTION_DAYS=30",
		"FEEDBACK_CONSENT_VERSION=v1",
	} {
		if !strings.Contains(string(env), want) {
			t.Errorf("synthetic env omits %q", want)
		}
	}
	if mode := mustStat(t, feedback.envFile).Mode().Perm(); mode != 0o600 {
		t.Errorf("provider env mode = %04o, want 0600", mode)
	}
	if feedback.subscriptionFile != "" {
		t.Errorf("subscription source was created on the host: %q", feedback.subscriptionFile)
	}
}

func TestFeedbackComposeInputsAndOverridesStayPrivateAndOrdered(t *testing.T) {
	assembly, err := writeFeedbackInputs(t.TempDir(), strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	assembly.proxyUsernameFile = "/private/proxy-username"
	assembly.proxyPasswordFile = "/private/proxy-password"
	assembly.subscriptionFile = "/private/subscription-url"
	auth := &authAssembly{port: 18090, login: "operator", passwordFile: "/private/operator-password", feedback: assembly}
	got := composeEnvironment(auth)
	for _, want := range []string{
		"FEEDBACK_IMAGE=" + candidateImageRef("feedback", assembly.ref, ""),
		"TELEGRAM_VPN_IMAGE=" + candidateImageRef("telegram-vpn", assembly.ref, ""),
		"FEEDBACK_URL=http://feedback:8084",
		"TELEGRAM_VPN_DNS=127.0.0.1",
		"TELEGRAM_PROXY_USERNAME_FILE=/private/proxy-username",
		"TELEGRAM_PROXY_PASSWORD_FILE=/private/proxy-password",
		"TELEGRAM_VPN_SUBSCRIPTION_URL_FILE=/private/subscription-url",
		"FEEDBACK_ENV_FILE=" + assembly.envFile,
	} {
		if !slices.Contains(got, want) {
			t.Errorf("Compose environment omits %q", want)
		}
	}
	for _, value := range got {
		if strings.Contains(value, "acceptance-dummy") || strings.Contains(value, "https://subscription.invalid") {
			t.Errorf("synthetic provider secret or subscription content reached Compose environment: %q", value)
		}
	}
	args := composeHTTPArgs("umir-http-test", "/factory", auth, "/tmp/generated.json")
	joined := strings.Join(args, " ")
	ordered := []string{"--file /factory/compose.yaml", "--file /factory/compose.auth.yaml", "--file /factory/compose.feedback.yaml", "--file /tmp/generated.json"}
	last := -1
	for _, piece := range ordered {
		index := strings.Index(joined, piece)
		if index <= last {
			t.Fatalf("Compose file order %q not preserved in %q", ordered, joined)
		}
		last = index
	}
}

func TestFeedbackImagesUseBothDockerfilesAndExactSourceMetadata(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	assembly, err := writeFeedbackInputs(t.TempDir(), sha)
	if err != nil {
		t.Fatal(err)
	}
	executor := &feedbackImageExecutor{sha: sha}
	if err := buildFeedbackImages(context.Background(), "/factory", "/source", assembly, executor); err != nil {
		t.Fatal(err)
	}
	if len(executor.builds) != 2 {
		t.Fatalf("Docker build calls = %d, want two", len(executor.builds))
	}
	wantFiles := []string{"/source/Dockerfile", "/source/Dockerfile.vpn"}
	for index, args := range executor.builds {
		if got := flagValue(args, "--file"); got != wantFiles[index] {
			t.Errorf("build %d Dockerfile = %q, want %q", index+1, got, wantFiles[index])
		}
		if got := flagValue(args, "--label"); got != "org.opencontainers.image.source="+feedbackRepository {
			t.Errorf("build %d source label = %q", index+1, got)
		}
		if !slices.Contains(args, "org.opencontainers.image.revision="+sha) {
			t.Errorf("build %d omits the exact revision label", index+1)
		}
		if args[len(args)-1] != "/source" {
			t.Errorf("build %d context = %q", index+1, args[len(args)-1])
		}
	}
	for _, name := range []string{"feedback", "telegram-vpn"} {
		image := assembly.images[name]
		if image.ID == "" || image.Revision != sha || image.Source != feedbackRepository || len(image.Healthcheck) < 2 {
			t.Errorf("candidate evidence for %s = %+v", name, image)
		}
	}
}

func TestFeedbackCredentialInitializerUsesAClosedNonNetworkContainerAndStdinJSON(t *testing.T) {
	executor := &feedbackInitializerExecutor{}
	assembly, err := writeFeedbackInputs(t.TempDir(), strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeFeedbackCredentials(context.Background(), "/factory", t.TempDir(), "fault-fixture-image", assembly, executor); err != nil {
		t.Fatal(err)
	}
	if executor.name != "docker" || executor.args[0] != "run" || !slices.Contains(executor.args, "--rm") || !slices.Contains(executor.args, "--network") ||
		!slices.Contains(executor.args, "none") || !slices.Contains(executor.args, "--user") || !slices.Contains(executor.args, "65532:65532") ||
		!slices.Contains(executor.args, "--entrypoint") || !slices.Contains(executor.args, "/feedback-fixture") || executor.args[len(executor.args)-1] != "fault-fixture-image" {
		t.Fatalf("credential initializer command = %v", executor.args)
	}
	if string(executor.stdin) != "{\"version\":1}\n" {
		t.Fatalf("credential initializer stdin = %q", executor.stdin)
	}
	for _, value := range executor.args {
		if strings.Contains(value, "acceptance-dummy") || strings.Contains(value, "subscription.invalid") {
			t.Fatalf("synthetic provider data reached the initializer argv: %q", value)
		}
	}
	if assembly.proxyUsernameFile == "" || assembly.proxyPasswordFile == "" || assembly.subscriptionFile == "" ||
		filepath.Dir(assembly.proxyUsernameFile) != filepath.Dir(assembly.proxyPasswordFile) || filepath.Dir(assembly.proxyUsernameFile) != filepath.Dir(assembly.subscriptionFile) {
		t.Fatalf("feedback secret paths = %q, %q, %q", assembly.proxyUsernameFile, assembly.proxyPasswordFile, assembly.subscriptionFile)
	}
}

func TestFeedbackOverlayRequiresOnlyVPNRunningNotHealthy(t *testing.T) {
	services := []string{"warehouse", "serving", "gateway", "frontend", "feedback", "telegram-vpn"}
	reports := make([]map[string]string, 0, len(services))
	for _, service := range services {
		health := "healthy"
		if service == "telegram-vpn" {
			health = "unhealthy"
		}
		reports = append(reports, map[string]string{"Service": service, "State": "running", "Health": health})
	}
	data, err := json.Marshal(reports)
	if err != nil {
		t.Fatal(err)
	}
	executor := &composeExecutor{output: data}
	if err := verifyRunningTopology(context.Background(), executor, "/factory", nil, []string{"compose"}, services, "telegram-vpn"); err != nil {
		t.Fatalf("running VPN with unavailable subscription blocked intake readiness: %v", err)
	}
	if err := verifyRunningTopology(context.Background(), executor, "/factory", nil, []string{"compose"}, services); err == nil {
		t.Fatal("unhealthy VPN was accepted without the feedback readiness exception")
	}
	for i := range reports {
		if reports[i]["Service"] == "telegram-vpn" {
			reports[i]["State"] = "exited"
		}
	}
	data, err = json.Marshal(reports)
	if err != nil {
		t.Fatal(err)
	}
	executor.output = data
	if err := verifyRunningTopology(context.Background(), executor, "/factory", nil, []string{"compose"}, services, "telegram-vpn"); err == nil {
		t.Fatal("stopped VPN was accepted")
	}
}

func TestFeedbackComposeRejectsPublishedSidecarPorts(t *testing.T) {
	port := 18090
	for _, service := range []string{"feedback", "telegram-vpn"} {
		report, err := json.Marshal([]map[string]any{{
			"Service": service, "Publishers": []map[string]any{{"URL": "127.0.0.1", "TargetPort": 8084, "PublishedPort": port}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := checkPublishedPorts(report, port, nil); err == nil || !strings.Contains(err.Error(), "only frontend may publish") {
			t.Errorf("%s published a host port without rejection: %v", service, err)
		}
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

type feedbackImageExecutor struct {
	sha    string
	builds [][]string
}

type feedbackInitializerExecutor struct {
	name  string
	args  []string
	stdin []byte
}

func (*feedbackInitializerExecutor) Run(context.Context, string, []string, string, ...string) ([]byte, []byte, error) {
	return nil, nil, errors.New("credential initializer must receive JSON on stdin")
}

func (e *feedbackInitializerExecutor) RunInput(_ context.Context, _ string, _ []string, stdin []byte, name string, args ...string) ([]byte, []byte, error) {
	e.name, e.args, e.stdin = name, slices.Clone(args), slices.Clone(stdin)
	metadata, err := json.Marshal(feedbackFixtureFiles{
		UsernamePath: "/run/feedback-fixture/proxy-username", PasswordPath: "/run/feedback-fixture/proxy-password",
		SubscriptionPath: "/run/feedback-fixture/subscription-url",
		UID:              65532, UsernameMode: 0o400, PasswordMode: 0o600, SubscriptionMode: 0o400,
	})
	return metadata, nil, err
}

func (e *feedbackImageExecutor) Run(_ context.Context, _ string, _ []string, name string, args ...string) ([]byte, []byte, error) {
	if name != "docker" {
		return nil, nil, fmt.Errorf("unexpected command %s", name)
	}
	if len(args) > 0 && args[0] == "build" {
		e.builds = append(e.builds, slices.Clone(args))
		return nil, nil, nil
	}
	if len(args) > 2 && args[0] == "image" && args[1] == "inspect" {
		image := map[string]any{
			"Id": "sha256:" + args[2],
			"Config": map[string]any{
				"Labels": map[string]string{
					"org.opencontainers.image.source":   feedbackRepository,
					"org.opencontainers.image.revision": e.sha,
				},
				"Healthcheck": map[string]any{"Test": []string{"CMD", "/server", "healthcheck"}},
			},
		}
		data, err := json.Marshal([]any{image})
		return data, nil, err
	}
	return nil, nil, fmt.Errorf("unexpected Docker arguments %v", args)
}

func flagValue(args []string, flag string) string {
	for index, arg := range args {
		if arg == flag && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}
