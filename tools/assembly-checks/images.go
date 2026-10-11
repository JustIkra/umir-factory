package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

type composeService struct {
	Image       string              `json:"image"`
	Environment map[string]string   `json:"environment"`
	Ports       []composePortConfig `json:"ports"`
	DependsOn   map[string]struct {
		Condition string `json:"condition"`
	} `json:"depends_on"`
}

type composePortConfig struct {
	Target    int    `json:"target"`
	Published string `json:"published"`
	HostIP    string `json:"host_ip"`
}

type composeConfig struct {
	Services map[string]composeService `json:"services"`
}

var imagePatterns = map[string]*regexp.Regexp{
	"gateway":   regexp.MustCompile(`^ghcr\.io/justikra/umir-gateway:sha-[0-9a-f]{40}$`),
	"serving":   regexp.MustCompile(`^ghcr\.io/justikra/umir-serving:sha-[0-9a-f]{40}$`),
	"warehouse": regexp.MustCompile(`^ghcr\.io/justikra/umir-warehouse:sha-[0-9a-f]{40}$`),
	"frontend":  regexp.MustCompile(`^ghcr\.io/justikra/umir-frontend:sha-[0-9a-f]{40}$`),
}

func checkImages(ctx context.Context, factoryDir string, executor commandExecutor) (map[string]string, error) {
	out, errOut, err := executor.Run(ctx, factoryDir, commandEnv(), "docker", "compose", "--env-file", ".env.example", "config", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("docker compose config failed: %s: %w", string(errOut), err)
	}
	var config composeConfig
	if err := json.Unmarshal(out, &config); err != nil {
		return nil, fmt.Errorf("parse Compose JSON: %w", err)
	}
	if err := validatePinnedImages(config); err != nil {
		return nil, err
	}
	images := make(map[string]string, 4)
	for service, details := range config.Services {
		images[service] = details.Image
	}
	return images, nil
}

func validatePinnedImages(config composeConfig) error {
	services := config.Services
	if len(services) != len(imagePatterns) {
		return errors.New("unexpected assembly services")
	}
	for service, pattern := range imagePatterns {
		details, exists := services[service]
		if !exists || !pattern.MatchString(details.Image) {
			return fmt.Errorf("invalid pinned image for %s", service)
		}
	}
	return nil
}
