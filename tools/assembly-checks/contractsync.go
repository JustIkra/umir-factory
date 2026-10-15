package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// contractClaim is one SHA-256 value a consumer copy vouches for: the field
// reported on drift, the composed role it must equal and the observed value.
type contractClaim struct{ field, role, got string }

// contractCopy is one consumer's copy of the composed contract. Auth and
// Frontend embed a composed file byte for byte; Gateway embeds a derived file
// that records the hashes of both composed files. For Gateway only those
// recorded hashes are compared with the composed files: the check cannot tell
// whether the derived operation table itself still matches its recorded source.
type contractCopy struct {
	consumer string
	path     string
	claims   func(data []byte) ([]contractClaim, error)
}

var contractCopies = []contractCopy{
	{"umir-auth", "umir-auth/internal/adapters/httpapi/contract.json", func(data []byte) ([]contractClaim, error) {
		return []contractClaim{{"private SHA-256", "private", backendDigest(data)}}, nil
	}},
	{"umir-gateway", "umir-gateway/internal/operatorhttp/auth-contract.json", func(data []byte) ([]contractClaim, error) {
		var derived struct {
			Source struct {
				Public  string `json:"publicSHA256"`
				Private string `json:"privateSHA256"`
			} `json:"source"`
		}
		if err := json.Unmarshal(data, &derived); err != nil {
			return nil, err
		}
		return []contractClaim{
			{"publicSHA256", "public", derived.Source.Public},
			{"privateSHA256", "private", derived.Source.Private},
		}, nil
	}},
	{"umir-frontend", "umir-frontend/api/backend-v2-public.json", func(data []byte) ([]contractClaim, error) {
		return []contractClaim{{"public SHA-256", "public", backendDigest(data)}}, nil
	}},
}

// contractSync compares the composed contract of a profile with the consumer
// copies under workspace and returns one line per drift. A non-nil error means
// the composed files themselves could not be read.
func contractSync(factoryDir, workspace, profile string) ([]string, error) {
	want := map[string]string{}
	for _, role := range []string{"public", "private"} {
		data, err := os.ReadFile(filepath.Join(factoryDir, "docs", "api", profile+"-v1-composed-"+role+".json"))
		if err != nil {
			return nil, fmt.Errorf("composed %s contract: %w", role, err)
		}
		want[role] = backendDigest(data)
	}
	var drift []string
	for _, copy := range contractCopies {
		prefix := copy.consumer + ": " + copy.path + ": "
		data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(copy.path)))
		if errors.Is(err, os.ErrNotExist) {
			drift = append(drift, prefix+"missing")
			continue
		}
		var claims []contractClaim
		if err == nil {
			claims, err = copy.claims(data)
		}
		if err != nil {
			drift = append(drift, prefix+"unreadable: "+err.Error())
			continue
		}
		for _, claim := range claims {
			if claim.got != want[claim.role] {
				drift = append(drift, fmt.Sprintf("%s%s differs: want %s, got %q", prefix, claim.field, want[claim.role], shortValue(claim.got)))
			}
		}
	}
	return drift, nil
}

// maxReportedValue bounds how much of a consumer-supplied value reaches the
// report, so a malformed copy cannot flood the output.
const maxReportedValue = 64

func shortValue(value string) string {
	runes := []rune(value)
	if len(runes) <= maxReportedValue {
		return value
	}
	return string(runes[:maxReportedValue])
}

func validateContractSyncOptions(options cliOptions) error {
	if options.profile != "durable" && options.profile != "registration" {
		return errors.New("--profile must be durable or registration")
	}
	if !filepath.IsAbs(options.workspace) {
		return errors.New("--workspace must be an absolute path")
	}
	if options.factoryDir != "" && !filepath.IsAbs(options.factoryDir) {
		return errors.New("--factory-dir must be an absolute path")
	}
	return nil
}

func runContractSync(factoryDir, workspace, profile string, stdout, stderr io.Writer) int {
	if factoryDir == "" {
		var err error
		if factoryDir, err = defaultFactoryDir(); err != nil {
			writeFailure(stderr, "%v\n", err)
			return 2
		}
	}
	drift, err := contractSync(factoryDir, workspace, profile)
	if err != nil {
		// The composed files of the profile are missing or unreadable: the
		// environment is wrong, which is not drift (1).
		writeFailure(stderr, "%v\n", err)
		return 2
	}
	if len(drift) > 0 {
		writeFailure(stderr, "%s\n", strings.Join(drift, "\n"))
		return 1
	}
	if err := writeMessage(stdout, "Contract copies match the %s composed contract\n", profile); err != nil {
		return 1
	}
	return 0
}

// defaultFactoryDir returns the nearest ancestor of the working directory that is a Git checkout.
func defaultFactoryDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("--factory-dir is required outside a Git checkout")
		}
		dir = parent
	}
}
