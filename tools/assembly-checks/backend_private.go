package main

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"time"
)

func validBackendPrivateURL(value string) bool {
	if len(value) > 32 {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port > 0 && port <= 65535 && value == "http://127.0.0.1:"+strconv.Itoa(port)
}

func readBackendOwnedCommand(ctx context.Context, dir string, executor commandExecutor, args ...string) ([]byte, error) {
	deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out []byte
	var err error
	if private, ok := executor.(backendPrivateExecutor); ok {
		out, err = private.RunPrivate(deadline, dir, commandEnv(), nil, "docker", args...)
	} else {
		out, _, err = executor.Run(deadline, dir, commandEnv(), "docker", args...)
	}
	if err != nil || len(out) > 262144 {
		return nil, errors.New("owned backend discovery output unavailable or exceeds bound")
	}
	return out, nil
}

func validateBackendPrivateServiceURLs(run backendRun, input backendInputs, containers []backendContainer) error {
	if run.Version != 2 {
		if len(run.PrivateServiceURLs) != 0 {
			return errors.New("private probes require backend-run version 2")
		}
		return nil
	}
	if input.Version != 2 || len(run.PrivateServiceURLs) != len(backendServices) {
		return errors.New("private probe service set is incomplete")
	}
	seen := map[string]bool{run.GatewayURL: true}
	for _, endpoint := range run.SinkURLs {
		seen[endpoint] = true
	}
	for _, name := range backendServices {
		endpoint := run.PrivateServiceURLs[name]
		if !validBackendPrivateURL(endpoint) || seen[endpoint] {
			return errors.New("invalid or aliased private probe endpoint")
		}
		seen[endpoint] = true
		matches := 0
		for _, c := range containers {
			if c.Service != name {
				continue
			}
			if c.ID == "" || c.ImageID == "" || c.ImageID != run.Images[name].ID || c.State != "running" || c.Health != "healthy" {
				return errors.New("private probe container identity differs")
			}
			for _, p := range c.Publishers {
				if p.TargetPort == input.Services[name].Port && p.PublishedPort > 0 {
					matches++
					if p.URL != "127.0.0.1" || p.Protocol != "tcp" || endpoint != "http://127.0.0.1:"+strconv.Itoa(p.PublishedPort) {
						return errors.New("private probe endpoint differs from owned mapping")
					}
				}
			}
		}
		if matches != 1 {
			return errors.New("private probe mapping is missing or duplicated")
		}
	}
	return nil
}

func discoverBackendPrivateServiceURLs(ctx context.Context, dir string, run backendRun, input backendInputs, expected []backendContainer, executor commandExecutor) (map[string]string, error) {
	deadline, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	urls := map[string]string{}
	for _, name := range backendServices {
		owned, err := backendContainers(deadline, dir, run.Project, run.ComposeFile, input, run.Images, executor, true)
		if err != nil {
			return nil, err
		}
		if len(owned) != len(expected) {
			return nil, errors.New("private probe container inventory changed")
		}
		for _, c := range owned {
			if !slices.ContainsFunc(expected, func(prior backendContainer) bool {
				return c.ID == prior.ID && c.Service == prior.Service && c.ImageID == prior.ImageID
			}) {
				return nil, errors.New("private probe container ownership changed")
			}
		}
		endpoint, err := backendEndpoint(deadline, dir, run.Project, run.ComposeFile, name, input.Services[name].Port, executor)
		if err != nil {
			return nil, err
		}
		urls[name] = endpoint
	}
	current, err := backendContainers(deadline, dir, run.Project, run.ComposeFile, input, run.Images, executor, true)
	if err != nil {
		return nil, err
	}
	for _, c := range current {
		if !slices.ContainsFunc(expected, func(prior backendContainer) bool {
			return c.ID == prior.ID && c.Service == prior.Service && c.ImageID == prior.ImageID
		}) {
			return nil, errors.New("private probe container changed after discovery")
		}
	}
	run.PrivateServiceURLs = urls
	if err := validateBackendPrivateServiceURLs(run, input, expected); err != nil {
		return nil, err
	}
	if err := validateBackendPrivateServiceURLs(run, input, current); err != nil {
		return nil, err
	}
	return urls, nil
}
