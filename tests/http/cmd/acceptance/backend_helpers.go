package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

var errBackendScenarioUnimplemented = errors.New("backend scenario unimplemented")

type backendScenarioInput struct {
	Session *backendSession
	Client  *backendClient
	Scopes  []backendScope
}

func backendScenarioContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 30*time.Second)
}
func backendCaseID(stage string, index int) string { return fmt.Sprintf("BE-%s-%02d", stage, index) }
func backendScenarioGroup(id string) (string, error) {
	parts := strings.Split(id, "-")
	if len(parts) != 3 || parts[0] != "BE" || len(parts[2]) != 2 {
		return "", faultproxy.ErrProtocol
	}
	index, err := strconv.Atoi(parts[2])
	if err != nil || index < 1 || backendCaseID(parts[1], index) != id {
		return "", faultproxy.ErrProtocol
	}
	switch parts[1] {
	case "E1":
		if index <= 14 {
			return "e12", nil
		}
	case "E2":
		if index <= 8 {
			return "e12", nil
		}
	case "E3":
		if index <= 12 {
			return "e34", nil
		}
	case "E4":
		if index <= 18 {
			return "e34", nil
		}
	case "E5":
		if index <= 15 {
			return "e5", nil
		}
	}
	return "", faultproxy.ErrProtocol
}
func dispatchBackendScenario(ctx context.Context, input backendScenarioInput, id string) error {
	group, err := backendScenarioGroup(id)
	if err != nil {
		return err
	}
	input.Scopes = slices.Clone(input.Scopes)
	switch group {
	case "e12":
		return runBackendE12(ctx, input, id)
	case "e34":
		return runBackendE34(ctx, input, id)
	case "e5":
		return runBackendE5(ctx, input, id)
	}
	return faultproxy.ErrProtocol
}
func (b *backendSession) freshScopes(ctx context.Context, zones []string) ([]backendScope, error) {
	if len(zones) < 1 || len(zones) > 4 {
		return nil, faultproxy.ErrProtocol
	}
	scopes := make([]backendScope, 0, len(zones))
	for i, zone := range zones {
		scope, err := b.organization(ctx, i, zone)
		if err != nil {
			return nil, err
		}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}
func backendPause(ctx context.Context) error {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return errors.New("scenario budget exhausted")
	case <-timer.C:
		return nil
	}
}
func (c *backendClient) checkpoint(ctx context.Context, target string) (fixtureClock, error) {
	clock, err := c.clock(ctx, target)
	if err != nil {
		return fixtureClock{}, err
	}
	data, err := c.control(ctx, target, "clock.checkpoint", map[string]any{"expectedInstanceID": clock.InstanceID, "expectedGeneration": clock.Generation})
	if err != nil {
		return fixtureClock{}, err
	}
	saved, err := validateCheckpoint(data, target, clock.InstanceID, c.run.Fixtures.FixtureID)
	if err != nil || !reflect.DeepEqual(saved, clock) {
		return fixtureClock{}, errors.New("ClockUnavailable")
	}
	return saved, nil
}
func backendRestartConfig(raw []byte, run backendRunDescriptor, target string, clock fixtureClock) ([]byte, error) {
	if faultproxy.SHA256(raw) != run.Fixtures.Owners[target].ConfigSHA256 || faultproxy.ValidateOperational("#/$defs/OwnerConfig", raw) != nil || clock.Owner != target || clock.FixtureID != run.Fixtures.FixtureID || clock.Source != "owner-injected" || !clock.Healthy || !faultproxy.UUID(clock.InstanceID) {
		return nil, errors.New("OwnershipMismatch")
	}
	parsed, err := faultproxy.StrictJSON(raw, 32)
	config, ok := parsed.(map[string]any)
	if err != nil || !ok || config["owner"] != target || config["project"] != run.Project || config["fixtureID"] != clock.FixtureID {
		return nil, errors.New("OwnershipMismatch")
	}
	config["initialInstant"] = clock.Instant
	config["clockGeneration"] = clock.Generation
	updated, err := json.Marshal(config)
	if err != nil || faultproxy.ValidateOperational("#/$defs/OwnerConfig", updated) != nil {
		return nil, faultproxy.ErrProtocol
	}
	return updated, nil
}
func replaceBackendConfig(path string, raw []byte) error {
	if !filepath.IsAbs(path) || filepath.Base(path) != "config.json" {
		return faultproxy.ErrProtocol
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return faultproxy.ErrProtocol
	}
	dir := filepath.Dir(path)
	for parent := dir; ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil {
			return faultproxy.ErrProtocol
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return faultproxy.ErrProtocol
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	f, err := os.CreateTemp(dir, ".clock-config-")
	if err != nil {
		return faultproxy.ErrProtocol
	}
	temp := f.Name()
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return errors.Join(faultproxy.ErrProtocol, os.Remove(temp))
	}
	if err := os.Rename(temp, path); err != nil {
		return errors.Join(faultproxy.ErrProtocol, os.Remove(temp))
	}
	return nil
}
func validateBackendRestartClock(before, after fixtureClock) error {
	if before.InstanceID == after.InstanceID || !faultproxy.UUID(after.InstanceID) || before.Owner != after.Owner || before.FixtureID != after.FixtureID || before.Instant != after.Instant || before.Generation != after.Generation || after.Source != "owner-injected" || !after.Healthy || !slices.Equal(before.InjectedConsumers, after.InjectedConsumers) {
		return errors.New("ClockUnavailable")
	}
	return nil
}

type backendVolume struct {
	Type, Name, Destination, Source string
	RW                              bool
}

func (c *backendClient) volumes(ctx context.Context, target, id string) ([]backendVolume, error) {
	raw, err := c.execute(ctx, nil, "container", "inspect", "--format", "{{json .Mounts}}", id)
	var mounts []backendVolume
	if err != nil || json.Unmarshal(raw, &mounts) != nil {
		return nil, errors.New("OwnershipMismatch")
	}
	volumes := []backendVolume{}
	configMounted := false
	expectedConfig := filepath.Join(filepath.Dir(c.run.Fixtures.HarnessPrivateRoot), target, "config")
	for _, mount := range mounts {
		if mount.Destination == "/run/umir-fixtures/config" {
			if configMounted || mount.Type != "bind" || mount.Source != expectedConfig || mount.RW {
				return nil, errors.New("OwnershipMismatch")
			}
			configMounted = true
		}
		if mount.Type == "volume" {
			volumes = append(volumes, mount)
		}
	}
	if !configMounted {
		return nil, errors.New("OwnershipMismatch")
	}
	if target == "auth" || target == "feedback" {
		if len(volumes) != 1 || volumes[0].Name != c.run.Project+"_"+target+"-state" || volumes[0].Destination != "/var/lib/umir-"+target {
			return nil, errors.New("OwnershipMismatch")
		}
	} else if len(volumes) != 0 {
		return nil, errors.New("unexpected durable business volume")
	}
	return volumes, nil
}
func (c *backendClient) restart(ctx context.Context, target string) (fixtureClock, error) {
	return c.restartMode(ctx, target, false)
}
func (c *backendClient) recreate(ctx context.Context, target string) (fixtureClock, error) {
	return c.restartMode(ctx, target, true)
}
func (c *backendClient) restartMode(ctx context.Context, target string, recreate bool) (fixtureClock, error) {
	release, lockErr := c.lockPrivate(ctx)
	if lockErr != nil {
		return fixtureClock{}, lockErr
	}
	defer release()
	if _, err := backendWaitMilliseconds(ctx, 0); err != nil || !slices.Contains(backendOwners, target) {
		return fixtureClock{}, faultproxy.ErrProtocol
	}
	container, err := c.ownership(ctx, target)
	if err != nil {
		return fixtureClock{}, err
	}
	if err := c.qualifyProbe(ctx, target); err != nil {
		return fixtureClock{}, err
	}
	volumes, err := c.volumes(ctx, target, container)
	if err != nil {
		return fixtureClock{}, err
	}
	saved, err := c.checkpoint(ctx, target)
	if err != nil {
		return fixtureClock{}, err
	}
	path := filepath.Join(filepath.Dir(c.run.Fixtures.HarnessPrivateRoot), target, "config", "config.json")
	raw, err := faultproxy.ReadPrivateFile(path, 16384)
	if err != nil {
		return fixtureClock{}, err
	}
	updated, err := backendRestartConfig(raw, c.run, target, saved)
	if err != nil {
		return fixtureClock{}, err
	}
	if id, err := c.ownership(ctx, target); err != nil || id != container {
		return fixtureClock{}, errors.New("OwnershipMismatch")
	}
	if c.unqualified == nil {
		c.unqualified = map[string]bool{}
	}
	c.unqualified[target] = true
	if _, err := c.execute(ctx, nil, c.compose("stop", "--timeout", "5", target)...); err != nil {
		return fixtureClock{}, err
	}
	if id, err := c.ownershipState(ctx, target, false); err != nil || id != container {
		return fixtureClock{}, errors.New("OwnershipMismatch")
	}
	afterVolumes, err := c.volumes(ctx, target, container)
	if err != nil || !reflect.DeepEqual(volumes, afterVolumes) {
		return fixtureClock{}, errors.New("OwnershipMismatch")
	}
	if err := replaceBackendConfig(path, updated); err != nil {
		return fixtureClock{}, err
	}
	owner := c.run.Fixtures.Owners[target]
	owner.ConfigSHA256 = faultproxy.SHA256(updated)
	c.run.Fixtures.Owners[target] = owner
	if _, err := c.ownershipState(ctx, target, false); err != nil {
		return fixtureClock{}, err
	}
	startArgs := c.compose("start", target)
	if recreate {
		startArgs = c.compose("up", "-d", "--no-deps", "--force-recreate", target)
	}
	if c.requalifying == nil {
		c.requalifying = map[string]bool{}
	}
	c.requalifying[target] = true
	defer delete(c.requalifying, target)
	if _, err := c.execute(ctx, nil, startArgs...); err != nil {
		return fixtureClock{}, err
	}
	if recreate {
		container, err = c.ownership(ctx, target)
		if err != nil {
			return fixtureClock{}, err
		}
	}
	if err := c.waitRestartReady(ctx, target, container); err != nil {
		return fixtureClock{}, err
	}
	if c.instances[target] == saved.InstanceID {
		return fixtureClock{}, errors.New("OwnershipMismatch")
	}
	id, err := c.ownership(ctx, target)
	if err != nil || id != container {
		return fixtureClock{}, errors.New("OwnershipMismatch")
	}
	afterVolumes, err = c.volumes(ctx, target, id)
	if err != nil || !reflect.DeepEqual(volumes, afterVolumes) {
		return fixtureClock{}, errors.New("OwnershipMismatch")
	}
	after, err := c.clock(ctx, target)
	if err != nil {
		return fixtureClock{}, err
	}
	if err := validateBackendRestartClock(saved, after); err != nil {
		return fixtureClock{}, err
	}
	if err := c.commitRestartProbe(ctx, target, container, saved, after); err != nil {
		return fixtureClock{}, err
	}
	c.restarts = append(c.restarts, backendRestartObservation{Target: target, Before: saved, After: after, VolumesPreserved: true, MemoryOnly: target == "warehouse" || target == "serving"})
	return after, nil
}

func (c *backendClient) waitRestartReady(ctx context.Context, target, container string) error {
	for {
		if _, err := backendWaitMilliseconds(ctx, 0); err != nil {
			return err
		}
		if id, err := c.ownership(ctx, target); err != nil || id != container {
			return errors.New("OwnershipMismatch")
		}
		_, err := c.control(ctx, target, "fixture.ready", map[string]any{})
		if err == nil {
			return nil
		}
		var transportErr *backendControlTransportError
		var fixtureErr *faultproxy.FixtureError
		retryable := errors.As(err, &transportErr) || errors.As(err, &fixtureErr) && fixtureErr.Retryable && fixtureErr.Effect == "none" && slices.Contains([]string{"NotReady", "WaitExpired"}, fixtureErr.Code)
		if ctx.Err() != nil || !retryable {
			return err
		}
		if err := backendPause(ctx); err != nil {
			return err
		}
	}
}

type backendRestartObservation struct {
	Target           string       `json:"target"`
	Before           fixtureClock `json:"before"`
	After            fixtureClock `json:"after"`
	VolumesPreserved bool         `json:"volumesPreserved"`
	MemoryOnly       bool         `json:"memoryOnly"`
}

func (c *backendClient) schedulerTick(ctx context.Context) (fixtureClock, error) {
	before, err := c.clock(ctx, "gateway")
	if err != nil {
		return fixtureClock{}, err
	}
	tick, err := faultproxy.NewUUID()
	if err != nil {
		return fixtureClock{}, err
	}
	data, err := c.control(ctx, "gateway", "scheduler.tick", map[string]any{"expectedInstanceID": before.InstanceID, "expectedGeneration": before.Generation, "tickID": tick})
	if err != nil {
		return fixtureClock{}, err
	}
	raw, err := json.Marshal(data["clock"])
	var observed fixtureClock
	if err != nil || json.Unmarshal(raw, &observed) != nil || data["tickID"] != tick || data["completed"] != true || !reflect.DeepEqual(before, observed) {
		return fixtureClock{}, errors.New("scheduler barrier unavailable")
	}
	after, err := c.clock(ctx, "gateway")
	if err != nil || !reflect.DeepEqual(before, after) {
		return fixtureClock{}, errors.New("ClockUnavailable")
	}
	return after, nil
}
func (c *backendClient) armOwnerFault(ctx context.Context, target, point, operation, command string, ttl time.Duration) (string, error) {
	if !slices.Contains([]string{"auth", "feedback", "warehouse"}, target) || point == "warehouse-after-prepare-before-analytics" && target != "warehouse" {
		return "", errors.New("UnsupportedOperation")
	}
	return c.armFault(ctx, target, "owner", map[string]any{"point": point, "operationID": operation, "commandID": command}, ttl)
}
func (c *backendClient) armSinkFault(ctx context.Context, target, mode string, selector map[string]any, ttl time.Duration) (string, error) {
	if !slices.Contains([]string{"smtp", "notification"}, target) {
		return "", errors.New("UnsupportedOperation")
	}
	return c.armFault(ctx, target, "sink", map[string]any{"mode": mode, "selector": selector}, ttl)
}
func (c *backendClient) armFault(ctx context.Context, target, kind string, data map[string]any, ttl time.Duration) (string, error) {
	if _, err := backendWaitMilliseconds(ctx, 0); err != nil {
		return "", err
	}
	deadline, _ := ctx.Deadline()
	remaining := time.Until(deadline)
	if ttl <= 0 || ttl > 30*time.Second {
		return "", faultproxy.ErrProtocol
	}
	if ttl > remaining {
		ttl = remaining
	}
	id, err := faultproxy.NewUUID()
	if err != nil {
		return "", err
	}
	data["faultID"] = id
	data["expireAfterMs"] = int(ttl / time.Millisecond)
	data["count"] = 1
	result, err := c.control(ctx, target, kind+".fault.arm", data)
	if err != nil {
		return "", err
	}
	if result["faultID"] != id || result["state"] != "armed" {
		return "", faultproxy.ErrProtocol
	}
	return id, nil
}
func (c *backendClient) faultState(ctx context.Context, target, id string) (map[string]any, error) {
	kind := "owner"
	if slices.Contains([]string{"smtp", "notification"}, target) {
		kind = "sink"
	}
	data, err := c.control(ctx, target, kind+".fault.read", map[string]any{"faultID": id})
	if err != nil {
		return nil, err
	}
	if data["faultID"] != id {
		return nil, faultproxy.ErrProtocol
	}
	return data, nil
}
func (c *backendClient) clearFault(ctx context.Context, target, id string) error {
	kind := "owner"
	if slices.Contains([]string{"smtp", "notification"}, target) {
		kind = "sink"
	}
	data, err := c.control(ctx, target, kind+".fault.clear", map[string]any{"faultID": id})
	if err != nil {
		return err
	}
	if data["faultID"] != id || data["state"] != "cleared" {
		return faultproxy.ErrProtocol
	}
	return nil
}
