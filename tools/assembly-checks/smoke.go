package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type smokeTargets struct {
	Web string
	API string
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

func runSmoke(ctx context.Context, client *http.Client, targets smokeTargets) error {
	if client == nil {
		client = defaultHTTPClient()
	}
	for _, endpoint := range []string{targets.Web, targets.API} {
		for _, service := range []string{"serving", "warehouse"} {
			path := "/api/" + service + "/healthz"
			if err := verifyHealth(ctx, client, strings.TrimRight(endpoint, "/")+path, "umir-"+service); err != nil {
				return err
			}
		}
	}
	if err := verifyFrontend(ctx, client, strings.TrimRight(targets.Web, "/")+"/"); err != nil {
		return err
	}
	return nil
}

func discoverSmokeTargets(ctx context.Context, factoryDir string, options cliOptions, executor commandExecutor) (smokeTargets, error) {
	targets := smokeTargets{Web: options.webURL, API: options.apiURL}
	if targets.Web != "" && targets.API != "" {
		return targets, nil
	}
	envArg := options.envFile
	if envArg != "" {
		resolvedEnvPath := envArg
		if !filepath.IsAbs(resolvedEnvPath) {
			resolvedEnvPath = filepath.Join(factoryDir, resolvedEnvPath)
		}
		info, err := os.Stat(resolvedEnvPath)
		if err != nil {
			return smokeTargets{}, fmt.Errorf("smoke env file %q: %w", resolvedEnvPath, err)
		}
		if !info.Mode().IsRegular() {
			return smokeTargets{}, fmt.Errorf("smoke env file is not a regular file: %s", resolvedEnvPath)
		}
		if filepath.IsAbs(envArg) {
			envArg = resolvedEnvPath
		}
	}
	if targets.Web == "" {
		web, err := discoverPublishedURL(ctx, factoryDir, envArg, "frontend", executor)
		if err != nil {
			return smokeTargets{}, err
		}
		targets.Web = web
	}
	if targets.API == "" {
		api, err := discoverPublishedURL(ctx, factoryDir, envArg, "gateway", executor)
		if err != nil {
			return smokeTargets{}, err
		}
		targets.API = api
	}
	return targets, nil
}

func discoverPublishedURL(ctx context.Context, factoryDir, envFile, service string, executor commandExecutor) (string, error) {
	args := []string{"compose"}
	if envFile != "" {
		args = append(args, "--env-file", envFile)
	}
	args = append(args, "port", service, "8080")
	output, stderr, err := executor.Run(ctx, factoryDir, commandEnv(), "docker", args...)
	if err != nil {
		return "", fmt.Errorf("docker compose port %s:8080 failed: %s: %w", service, strings.TrimSpace(string(stderr)), err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "", fmt.Errorf("no published port for %s:8080", service)
	}
	return publishedURL(strings.TrimSpace(lines[0]), service, 8080)
}

func publishedURL(address, service string, targetPort int) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("invalid published port for %s:%d %q: %w", service, targetPort, address, err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("invalid published port for %s:%d: %q", service, targetPort, address)
	}
	if host == "" {
		host = "127.0.0.1"
	} else if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		if ip.To4() != nil {
			host = "127.0.0.1"
		} else {
			host = "::1"
		}
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(portNumber)), nil
}

func parseSmokeURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("must be an http or https URL with a host and no credentials or fragment")
	}
	return strings.TrimRight(value, "/"), nil
}

func verifyHealth(ctx context.Context, client *http.Client, url, service string) (result error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request %s: %w", url, err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("close response body from %s: %w", url, err))
		}
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("read %s: %w", url, err)
	}
	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("invalid health JSON from %s: %w", url, err)
	}
	if response.StatusCode != http.StatusOK || len(payload) != 2 || payload["status"] != "ok" || payload["service"] != service {
		return fmt.Errorf("unexpected response from %s", url)
	}
	return nil
}

func verifyFrontend(ctx context.Context, client *http.Client, url string) (result error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request %s: %w", url, err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("close response body from %s: %w", url, err))
		}
	}()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("read %s: %w", url, err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `id="root"`) {
		return fmt.Errorf("frontend entry point is unavailable at %s", url)
	}
	return nil
}
