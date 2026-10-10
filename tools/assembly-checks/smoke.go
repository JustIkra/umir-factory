package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type smokeTargets struct {
	Gateways []string
	Frontend string
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

func runSmoke(ctx context.Context, client *http.Client, targets smokeTargets) error {
	if client == nil {
		client = defaultHTTPClient()
	}
	for _, gateway := range targets.Gateways {
		for _, service := range []string{"serving", "warehouse"} {
			path := "/api/" + service + "/healthz"
			if err := verifyHealth(ctx, client, strings.TrimRight(gateway, "/")+path, "umir-"+service); err != nil {
				return err
			}
		}
	}
	if err := verifyFrontend(ctx, client, strings.TrimRight(targets.Frontend, "/")+"/"); err != nil {
		return err
	}
	return nil
}

func verifyHealth(ctx context.Context, client *http.Client, url, service string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request %s: %w", url, err)
	}
	defer response.Body.Close()
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

func verifyFrontend(ctx context.Context, client *http.Client, url string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request %s: %w", url, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("read %s: %w", url, err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `id="root"`) {
		return fmt.Errorf("frontend entry point is unavailable at %s", url)
	}
	return nil
}
