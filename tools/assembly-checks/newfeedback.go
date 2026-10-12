package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	feedbackOverrideFile = "compose.feedback.yaml"
	feedbackMarker       = "x-umir-feedback-acceptance"
	feedbackMarkerLine   = feedbackMarker + ": candidate"
	feedbackRepository   = "https://github.com/JustIkra/umir-feedback"
	feedbackServicePort  = "http://feedback:8084"
)

type feedbackAssembly struct {
	ref               string
	feedbackImage     string
	vpnImage          string
	envFile           string
	subscriptionFile  string
	proxyUsernameFile string
	proxyPasswordFile string
	dns               string
	images            map[string]imageEvidence
}

type feedbackFixtureFiles struct {
	UsernamePath     string `json:"usernamePath"`
	PasswordPath     string `json:"passwordPath"`
	SubscriptionPath string `json:"subscriptionPath"`
	UID              int    `json:"uid"`
	UsernameMode     uint32 `json:"usernameMode"`
	PasswordMode     uint32 `json:"passwordMode"`
	SubscriptionMode uint32 `json:"subscriptionMode"`
}

func verifyFeedbackMarker(factoryDir string) error {
	path := filepath.Join(factoryDir, feedbackOverrideFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", feedbackOverrideFile, err)
	}
	markers := 0
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, feedbackMarker+":") {
			if line != feedbackMarkerLine {
				return fmt.Errorf("%s declares %q, want %q", feedbackOverrideFile, line, feedbackMarkerLine)
			}
			markers++
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", feedbackOverrideFile, err)
	}
	if markers != 1 {
		return fmt.Errorf("%s must declare %q exactly once, found %d", feedbackOverrideFile, feedbackMarkerLine, markers)
	}
	return nil
}

func writeFeedbackInputs(tempDir, ref string) (*feedbackAssembly, error) {
	if !isSHA(ref) {
		return nil, errors.New("feedback ref must be exactly 40 lowercase hexadecimal characters")
	}
	envFile := filepath.Join(tempDir, "feedback.env")
	const providerEnv = "FEEDBACK_SMTP_HOST=127.0.0.1\n" +
		"FEEDBACK_SMTP_PORT=25\n" +
		"FEEDBACK_SMTP_TLS_MODE=starttls\n" +
		"FEEDBACK_SMTP_USERNAME=acceptance-dummy\n" +
		"FEEDBACK_SMTP_PASSWORD=acceptance-dummy\n" +
		"FEEDBACK_MAIL_FROM=feedback@invalid.example\n" +
		"FEEDBACK_MAIL_TO=operator@invalid.example\n" +
		"FEEDBACK_TELEGRAM_BOT_TOKEN=123456789:acceptance-dummy\n" +
		"FEEDBACK_TELEGRAM_CHAT_ID=-1000000000001\n" +
		"FEEDBACK_MAX_STORE_BYTES=1048576\n" +
		"FEEDBACK_RETENTION_DAYS=30\n" +
		"FEEDBACK_CONSENT_VERSION=v1\n"
	if err := os.WriteFile(envFile, []byte(providerEnv), 0o600); err != nil {
		return nil, fmt.Errorf("write synthetic feedback provider config: %w", err)
	}
	return &feedbackAssembly{
		ref: ref, feedbackImage: candidateImageRef("feedback", ref, ""), vpnImage: candidateImageRef("telegram-vpn", ref, ""),
		envFile: envFile, dns: "127.0.0.1",
	}, nil
}

func (a *feedbackAssembly) composeEnv() []string {
	return []string{
		"FEEDBACK_IMAGE=" + a.feedbackImage,
		"TELEGRAM_VPN_IMAGE=" + a.vpnImage,
		"FEEDBACK_COMMIT=" + a.ref,
		"FEEDBACK_ENV_FILE=" + a.envFile,
		"TELEGRAM_VPN_SUBSCRIPTION_URL_FILE=" + a.subscriptionFile,
		"TELEGRAM_PROXY_USERNAME_FILE=" + a.proxyUsernameFile,
		"TELEGRAM_PROXY_PASSWORD_FILE=" + a.proxyPasswordFile,
		"TELEGRAM_VPN_DNS=" + a.dns,
		"FEEDBACK_URL=" + feedbackServicePort,
	}
}

func prepareFeedbackAssembly(ctx context.Context, executor commandExecutor, factoryDir, tempDir, ref, fixtureImage string) (*feedbackAssembly, error) {
	if err := verifyFeedbackMarker(factoryDir); err != nil {
		return nil, err
	}
	sourceDir, err := candidateSource(ctx, factoryDir, "feedback", ref, filepath.Join(tempDir, "feedback"), executor)
	if err != nil {
		return nil, fmt.Errorf("resolve feedback candidate %s: %w", ref, err)
	}
	if _, err := os.Stat(filepath.Join(sourceDir, "Dockerfile.vpn")); err != nil {
		return nil, fmt.Errorf("feedback candidate Dockerfile.vpn: %w", err)
	}
	assembly, err := writeFeedbackInputs(tempDir, ref)
	if err != nil {
		return nil, err
	}
	if err := buildFeedbackImages(ctx, factoryDir, sourceDir, assembly, executor); err != nil {
		return nil, err
	}
	if err := initializeFeedbackCredentials(ctx, factoryDir, tempDir, fixtureImage, assembly, executor); err != nil {
		return nil, err
	}
	return assembly, nil
}

func buildFeedbackImages(ctx context.Context, factoryDir, sourceDir string, assembly *feedbackAssembly, executor commandExecutor) error {
	if !isSHA(assembly.ref) {
		return errors.New("feedback ref must be exactly 40 lowercase hexadecimal characters")
	}
	source := feedbackRepository
	for _, image := range []struct{ name, dockerfile, tag string }{
		{"feedback", "Dockerfile", assembly.feedbackImage},
		{"telegram-vpn", "Dockerfile.vpn", assembly.vpnImage},
	} {
		args := []string{
			"build", "--file", filepath.Join(sourceDir, image.dockerfile),
			"--label", "org.opencontainers.image.source=" + source,
			"--label", "org.opencontainers.image.revision=" + assembly.ref,
			"--tag", image.tag, sourceDir,
		}
		_, stderr, err := executor.Run(ctx, factoryDir, commandEnv(), "docker", args...)
		if err != nil {
			return fmt.Errorf("build %s candidate image: %s: %w", image.name, strings.TrimSpace(string(stderr)), err)
		}
		if err := verifyDockerImage(ctx, executor, factoryDir, image.tag, source, assembly.ref, true); err != nil {
			return fmt.Errorf("verify %s candidate image: %w", image.name, err)
		}
		inspected, err := inspectDockerImage(ctx, executor, factoryDir, image.tag)
		if err != nil {
			return fmt.Errorf("inspect %s candidate image: %w", image.name, err)
		}
		// The caller records the exact identities after each image is built.
		assembly.recordImage(image.name, makeImageEvidence(image.tag, inspected))
	}
	return nil
}

func (a *feedbackAssembly) recordImage(name string, image imageEvidence) {
	if a.images == nil {
		a.images = make(map[string]imageEvidence, 2)
	}
	a.images[name] = image
}

func initializeFeedbackCredentials(ctx context.Context, factoryDir, tempDir, fixtureImage string, assembly *feedbackAssembly, executor commandExecutor) error {
	runner, ok := executor.(inputRunner)
	if !ok {
		return errors.New("the command executor cannot initialize private feedback credentials on stdin")
	}
	directory, err := os.MkdirTemp(tempDir, "feedback-credentials-")
	if err != nil {
		return fmt.Errorf("create private feedback credential directory: %w", err)
	}
	if err := os.Chmod(directory, 0o1777); err != nil {
		return fmt.Errorf("prepare feedback credential directory: %w", err)
	}
	mount := "type=bind,source=" + directory + ",target=/run/feedback-fixture"
	stdout, stderr, err := runner.RunInput(ctx, factoryDir, commandEnv(), []byte("{\"version\":1}\n"), "docker", "run", "--rm", "--interactive", "--network", "none", "--user", "65532:65532", "--mount", mount, "--entrypoint", "/feedback-fixture", fixtureImage)
	if err != nil {
		return fmt.Errorf("initialize synthetic proxy credentials: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	var files feedbackFixtureFiles
	if err := json.Unmarshal(stdout, &files); err != nil {
		return fmt.Errorf("decode feedback fixture metadata: %w", err)
	}
	if files.UID != 65532 || files.UsernameMode != 0o400 || files.PasswordMode != 0o600 || files.SubscriptionMode != 0o400 ||
		files.UsernamePath != "/run/feedback-fixture/proxy-username" || files.PasswordPath != "/run/feedback-fixture/proxy-password" ||
		files.SubscriptionPath != "/run/feedback-fixture/subscription-url" {
		return errors.New("feedback fixture files do not have the required owner, paths, or modes")
	}
	assembly.proxyUsernameFile = filepath.Join(directory, "proxy-username")
	assembly.proxyPasswordFile = filepath.Join(directory, "proxy-password")
	assembly.subscriptionFile = filepath.Join(directory, "subscription-url")
	return nil
}
