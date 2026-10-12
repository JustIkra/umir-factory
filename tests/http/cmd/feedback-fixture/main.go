package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const credentialDirectory = "/run/feedback-fixture"

type fixtureFiles struct {
	UsernamePath     string `json:"usernamePath"`
	PasswordPath     string `json:"passwordPath"`
	SubscriptionPath string `json:"subscriptionPath"`
	UID              int    `json:"uid"`
	UsernameMode     uint32 `json:"usernameMode"`
	PasswordMode     uint32 `json:"passwordMode"`
	SubscriptionMode uint32 `json:"subscriptionMode"`
}

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
			os.Exit(1)
		}
		os.Exit(1)
	}
}

func run(input io.Reader, output io.Writer) error {
	return runAt(credentialDirectory, input, output, true)
}

func runAt(directory string, input io.Reader, output io.Writer, requireContainerUID bool) error {
	if requireContainerUID && os.Getuid() != 65532 {
		return errors.New("feedback fixture must run as uid 65532")
	}
	if err := validateRequest(input); err != nil {
		return err
	}
	files, err := initialize(directory)
	if err != nil {
		return fmt.Errorf("create feedback proxy credentials: %w", err)
	}
	if requireContainerUID && files.UID != 65532 || files.UsernameMode != 0o400 || files.PasswordMode != 0o600 || files.SubscriptionMode != 0o400 {
		return errors.New("feedback proxy credentials have an unexpected owner or mode")
	}
	if err := json.NewEncoder(output).Encode(files); err != nil {
		return errors.New("write feedback proxy credential metadata")
	}
	return nil
}

func validateRequest(input io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(input, 4097))
	if err != nil || len(data) > 4096 {
		return errors.New("feedback fixture request is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request struct {
		Version int `json:"version"`
	}
	if err := decoder.Decode(&request); err != nil {
		return errors.New("invalid feedback fixture request")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("feedback fixture request must contain exactly one object")
	}
	if request.Version != 1 {
		return errors.New("unsupported feedback fixture request version")
	}
	return nil
}

func initialize(directory string) (fixtureFiles, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fixtureFiles{}, errors.New("credential directory is unavailable")
	}
	username, err := proxyCredential()
	if err != nil {
		return fixtureFiles{}, err
	}
	password, err := proxyCredential()
	if err != nil {
		return fixtureFiles{}, err
	}
	usernamePath := filepath.Join(directory, "proxy-username")
	passwordPath := filepath.Join(directory, "proxy-password")
	subscriptionPath := filepath.Join(directory, "subscription-url")
	if err := writeCredential(usernamePath, username, 0o400); err != nil {
		return fixtureFiles{}, err
	}
	if err := writeCredential(passwordPath, password, 0o600); err != nil {
		return fixtureFiles{}, errors.Join(err, removeCredentialFiles(usernamePath))
	}
	if err := writeCredential(subscriptionPath, "https://subscription.invalid/factory-acceptance", 0o400); err != nil {
		return fixtureFiles{}, errors.Join(err, removeCredentialFiles(usernamePath, passwordPath))
	}
	usernameInfo, err := os.Lstat(usernamePath)
	if err != nil {
		return fixtureFiles{}, errors.Join(errors.New("inspect proxy username file"), removeCredentialFiles(usernamePath, passwordPath, subscriptionPath))
	}
	passwordInfo, err := os.Lstat(passwordPath)
	if err != nil {
		return fixtureFiles{}, errors.Join(errors.New("inspect proxy password file"), removeCredentialFiles(usernamePath, passwordPath, subscriptionPath))
	}
	subscriptionInfo, err := os.Lstat(subscriptionPath)
	if err != nil {
		return fixtureFiles{}, errors.Join(errors.New("inspect synthetic subscription file"), removeCredentialFiles(usernamePath, passwordPath, subscriptionPath))
	}
	usernameStat, usernameOK := usernameInfo.Sys().(*syscall.Stat_t)
	passwordStat, passwordOK := passwordInfo.Sys().(*syscall.Stat_t)
	subscriptionStat, subscriptionOK := subscriptionInfo.Sys().(*syscall.Stat_t)
	if !usernameOK || !passwordOK || !subscriptionOK || usernameStat.Uid != uint32(os.Getuid()) || passwordStat.Uid != uint32(os.Getuid()) || subscriptionStat.Uid != uint32(os.Getuid()) ||
		!usernameInfo.Mode().IsRegular() || !passwordInfo.Mode().IsRegular() ||
		!subscriptionInfo.Mode().IsRegular() || usernameInfo.Mode().Perm() != 0o400 || passwordInfo.Mode().Perm() != 0o600 || subscriptionInfo.Mode().Perm() != 0o400 {
		return fixtureFiles{}, errors.Join(errors.New("feedback fixture files have unexpected ownership or permissions"), removeCredentialFiles(usernamePath, passwordPath, subscriptionPath))
	}
	return fixtureFiles{
		UsernamePath:     "/run/feedback-fixture/proxy-username",
		PasswordPath:     "/run/feedback-fixture/proxy-password",
		SubscriptionPath: "/run/feedback-fixture/subscription-url",
		UID:              int(usernameStat.Uid),
		UsernameMode:     uint32(usernameInfo.Mode().Perm()),
		PasswordMode:     uint32(passwordInfo.Mode().Perm()),
		SubscriptionMode: uint32(subscriptionInfo.Mode().Perm()),
	}, nil
}

func removeCredentialFiles(paths ...string) error {
	var result error
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, fmt.Errorf("remove proxy credential file: %w", err))
		}
	}
	return result
}

func writeCredential(path, value string, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return errors.New("create proxy credential file")
	}
	written, writeErr := io.WriteString(file, value)
	if writeErr == nil && written != len(value) {
		writeErr = io.ErrShortWrite
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(errors.New("write proxy credential file"), writeErr, closeErr, removeCredentialFiles(path))
	}
	return nil
}

func proxyCredential() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", errors.New("generate proxy credential")
	}
	return strings.ToLower(hex.EncodeToString(data[:])), nil
}
