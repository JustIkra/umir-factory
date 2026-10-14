package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var backendSecretNames = []string{"gateway-auth", "auth-warehouse", "auth-serving", "auth-gateway", "gateway-warehouse", "gateway-serving", "gateway-feedback", "serving-warehouse", "serving-auth", "bootstrap-password"}

func validateBackendEnvironment(env map[string]string, service string) error {
	for key, value := range env {
		if key == "" || strings.ContainsAny(key, "=\r\n\x00") || strings.ContainsAny(value, "\r\n\x00$") {
			return errors.New("invalid backend environment entry")
		}
		if strings.HasPrefix(value, "{{") {
			if value == "{{origin}}" {
				continue
			}
			if strings.HasPrefix(value, "{{url:") && strings.HasSuffix(value, "}}") {
				name := strings.TrimSuffix(strings.TrimPrefix(value, "{{url:"), "}}")
				if slices.Contains(append(slices.Clone(backendServices), "smtp", "notification"), name) {
					continue
				}
			}
			if strings.HasPrefix(value, "{{secret:") && strings.HasSuffix(value, "}}") {
				name := strings.TrimSuffix(strings.TrimPrefix(value, "{{secret:"), "}}")
				if slices.Contains(backendSecretNames, name) && (strings.Contains(name, service) || name == "bootstrap-password" && service == "auth") {
					continue
				}
			}
			return errors.New("unsupported or out-of-scope environment template")
		}
		upper := strings.ToUpper(key)
		for _, sensitive := range []string{"PASSWORD", "SECRET", "TOKEN", "KEY", "PRIVATE", "SIGNING"} {
			if strings.Contains(upper, sensitive) {
				return errors.New("credentials must use an allowed synthetic secret template")
			}
		}
		if strings.Contains(value, "{{") || strings.Contains(value, "}}") {
			return errors.New("environment templates must occupy the whole value")
		}
		if strings.Contains(value, "@") {
			parts := strings.Split(value, "@")
			if len(parts) != 2 || !strings.HasSuffix(parts[1], ".invalid") {
				return errors.New("only synthetic .invalid recipient addresses are allowed")
			}
		}
		if strings.Contains(value, "://") {
			u, err := url.Parse(value)
			if err != nil || u.Scheme != "http" || u.User != nil || !slices.Contains(append(slices.Clone(backendServices), "smtp", "notification"), u.Hostname()) {
				return errors.New("external delivery or service URLs are forbidden")
			}
		}
	}
	return nil
}
func backendSecrets() (map[string]string, error) {
	result := map[string]string{}
	for _, name := range backendSecretNames {
		var token string
		var err error
		if name == "bootstrap-password" {
			token, err = backendShortSecret()
		} else {
			token, err = randomToken()
		}
		if err != nil {
			return nil, err
		}
		result[name] = token
	}
	return result, nil
}
func backendShortSecret() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
func backendURLs(input backendInputs) map[string]string {
	result := map[string]string{}
	for name, s := range input.Services {
		result[name] = "http://" + name + ":" + strconv.Itoa(s.Port)
	}
	for name, s := range input.Sinks {
		result[name] = "http://" + name + ":" + strconv.Itoa(s.Port)
	}
	return result
}
func writeBackendTopology(temp, project string, input backendInputs, images map[string]imageEvidence, secrets map[string]string, origin string, gatewayPort int) (string, map[string]any, error) {
	services := map[string]any{}
	facts := map[string]any{}
	urls := backendURLs(input)
	for _, name := range backendServices {
		source := input.Services[name]
		keys := []string{}
		for key := range source.Environment {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var environment strings.Builder
		for _, key := range keys {
			value := source.Environment[key]
			if value == "{{origin}}" {
				value = origin
			}
			for name, host := range urls {
				if value == "{{url:"+name+"}}" {
					value = host
				}
			}
			for name, secret := range secrets {
				if value == "{{secret:"+name+"}}" {
					value = secret
				}
			}
			if _, err := environment.WriteString(key + "=" + value + "\n"); err != nil {
				return "", nil, err
			}
		}
		envFile := filepath.Join(temp, name+".env")
		if err := os.WriteFile(envFile, []byte(environment.String()), 0o600); err != nil {
			return "", nil, err
		}
		s := map[string]any{"image": images[name].Ref, "pull_policy": "never", "restart": "no", "networks": []string{"private"}, "env_file": []any{map[string]any{"path": envFile, "format": "raw"}}, "healthcheck": map[string]any{"test": backendComposeLiterals(source.Healthcheck), "interval": "1s", "timeout": "2s", "retries": 30, "start_period": "2s"}, "labels": map[string]string{"io.umir.backend.run": project}}
		if len(source.Command) > 0 {
			s["command"] = backendComposeLiterals(source.Command)
		}
		if name == "auth" || name == "feedback" {
			s["volumes"] = []any{map[string]any{"type": "volume", "source": name + "-state", "target": source.StatePath}}
		}
		if name == "gateway" {
			s["ports"] = []any{backendPublishedPort(source.PublicPort, strconv.Itoa(gatewayPort))}
		}
		if input.Version == 2 {
			ports := []any{}
			if name == "gateway" {
				ports = append(ports, backendPublishedPort(source.PublicPort, strconv.Itoa(gatewayPort)))
			}
			s["ports"] = append(ports, backendPublishedPort(source.Port, "0"))
		}
		services[name] = s
		facts[name] = map[string]any{"image": images[name].Ref, "environmentKeys": keys, "privatePort": source.Port, "publicPort": source.PublicPort, "durableState": name == "auth" || name == "feedback"}
	}
	for _, name := range []string{"smtp", "notification"} {
		sink := input.Sinks[name]
		image := sink.Image
		if built, ok := images[name]; ok {
			image = built.Ref
		}
		s := map[string]any{"image": image, "restart": "no", "networks": []string{"private"}, "ports": []any{backendPublishedPort(sink.ControlPort, "0")}, "labels": map[string]string{"io.umir.backend.run": project}}
		if len(sink.Command) > 0 {
			s["command"] = backendComposeLiterals(sink.Command)
		}
		services[name] = s
		facts[name] = map[string]any{"image": image, "syntheticDeliveryOnly": true, "controlPort": sink.ControlPort}
	}
	if input.Version == 2 {
		if err := mountBackendFixtures(temp, services); err != nil {
			return "", nil, err
		}
	}
	topology := map[string]any{"name": project, "services": services, "volumes": map[string]any{"auth-state": map[string]any{}, "feedback-state": map[string]any{}}, "networks": map[string]any{"private": map[string]any{"internal": true}}}
	path := filepath.Join(temp, "compose.json")
	return path, facts, writeBackendJSON(path, topology)
}

type backendDeliveryConfig struct {
	SMTPHost           string         `json:"smtpHost"`
	SMTPPort           int            `json:"smtpPort"`
	SMTPTLS            string         `json:"smtpTLS"`
	SMTPCAFile         backendFileRef `json:"smtpCAFile"`
	NotificationProxy  string         `json:"notificationProxy"`
	NotificationCAFile backendFileRef `json:"notificationCAFile"`
}
type backendOwnerConfig struct {
	Version               int                       `json:"version"`
	Protocol              string                    `json:"protocol"`
	Project               string                    `json:"project"`
	Owner                 string                    `json:"owner"`
	FixtureID             string                    `json:"fixtureID"`
	InitialInstant        string                    `json:"initialInstant"`
	ClockGeneration       string                    `json:"clockGeneration"`
	ControlCredentialFile backendFileRef            `json:"controlCredentialFile"`
	CredentialFiles       map[string]backendFileRef `json:"credentialFiles"`
	ControlSocket         string                    `json:"controlSocket"`
	PrivatePort           int                       `json:"privatePort"`
	PublicPort            int                       `json:"publicPort"`
	Origin                string                    `json:"origin"`
	ServiceURLs           map[string]string         `json:"serviceURLs"`
	Delivery              backendDeliveryConfig     `json:"delivery"`
	StateFile             string                    `json:"stateFile"`
	Bootstrap             *backendBootstrap         `json:"bootstrap"`
}
type backendSinkConfig struct {
	Version               int                       `json:"version"`
	Protocol              string                    `json:"protocol"`
	Project               string                    `json:"project"`
	Target                string                    `json:"target"`
	ControlPort           int                       `json:"controlPort"`
	DeliveryPort          int                       `json:"deliveryPort"`
	ControlCredentialFile backendFileRef            `json:"controlCredentialFile"`
	CredentialFiles       map[string]backendFileRef `json:"credentialFiles"`
	CaptureLimit          int                       `json:"captureLimit"`
	MessageBytes          int                       `json:"messageBytes"`
	TotalCaptureBytes     int                       `json:"totalCaptureBytes"`
}
type backendRunOwner struct {
	Profile               string         `json:"profile"`
	ConfigSHA256          string         `json:"configSHA256"`
	ServerBinarySHA256    string         `json:"serverBinarySHA256"`
	ControlBinarySHA256   string         `json:"controlBinarySHA256"`
	ConfigPath            string         `json:"configPath"`
	ControlSocket         string         `json:"controlSocket"`
	ControlCredentialFile backendFileRef `json:"controlCredentialFile"`
	Capabilities          []string       `json:"capabilities"`
}
type backendRunFixtures struct {
	Protocol               string                     `json:"protocol"`
	BindingsSHA256         string                     `json:"bindingsSHA256"`
	FixtureID              string                     `json:"fixtureID"`
	InitialInstant         string                     `json:"initialInstant"`
	InitialGeneration      string                     `json:"initialGeneration"`
	HarnessPrivateRoot     string                     `json:"harnessPrivateRoot"`
	Owners                 map[string]backendRunOwner `json:"owners"`
	BootstrapBindingSHA256 string                     `json:"bootstrapBindingSHA256"`
}

var backendCredentialTargets = map[string][]string{
	"gateway-auth": {"gateway", "auth"}, "auth-warehouse": {"auth", "warehouse"}, "auth-serving": {"auth", "serving"}, "auth-gateway": {"auth", "gateway"},
	"gateway-warehouse": {"gateway", "warehouse"}, "gateway-serving": {"gateway", "serving"}, "gateway-feedback": {"gateway", "feedback"}, "serving-warehouse": {"serving", "warehouse"},
	"serving-auth": {"serving", "auth"}, "bootstrap-password": {"auth"}, "smtp-username": {"auth", "feedback", "smtp"}, "smtp-password": {"auth", "feedback", "smtp"},
	"proxy-username": {"feedback", "notification"}, "proxy-password": {"feedback", "notification"}, "telegram-bot": {"feedback", "notification"},
	"synthetic-ca.pem": {"auth", "feedback", "smtp", "notification"}, "smtp-tls-cert": {"smtp"}, "smtp-tls-key": {"smtp"},
	"notification-tls-cert": {"notification"}, "notification-tls-key": {"notification"},
	"fixture-auth": {"auth"}, "fixture-feedback": {"feedback"}, "fixture-warehouse": {"warehouse"}, "fixture-serving": {"serving"},
	"fixture-gateway": {"gateway"}, "fixture-smtp": {"smtp"}, "fixture-notification": {"notification"},
}

func backendCredentialRef(target, name string) backendFileRef {
	return backendFileRef{target, "/run/umir-fixtures/secrets/" + name}
}
func backendConsumerRefs(target string) map[string]backendFileRef {
	refs := map[string]backendFileRef{}
	for name, consumers := range backendCredentialTargets {
		allowed := slices.Contains(consumers, target)
		if target == "harness" {
			allowed = slices.Contains(backendSecretNames, name) || strings.HasPrefix(name, "fixture-")
		}
		if allowed {
			refs[name] = backendCredentialRef(target, name)
		}
	}
	return refs
}
func backendServiceRefs(refs map[string]backendFileRef) map[string]backendFileRef {
	result := map[string]backendFileRef{}
	for name, ref := range refs {
		if !strings.HasPrefix(name, "fixture-") {
			result[name] = ref
		}
	}
	return result
}
func validateBackendServiceMaterial(material map[string][]byte) error {
	names := slices.Clone(backendSecretNames)
	for _, target := range append(slices.Clone(backendServices), "smtp", "notification") {
		names = append(names, "fixture-"+target)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if name == "bootstrap-password" {
			continue
		}
		value := string(material[name])
		if !isSHA256(value) || seen[value] {
			return errors.New("invalid or reused synthetic service/control credential")
		}
		seen[value] = true
	}
	return nil
}
func backendSyntheticCredentials(secrets map[string]string) (map[string][]byte, error) {
	if len(secrets) != len(backendSecretNames) {
		return nil, errors.New("credential set differs from reviewed names")
	}
	seen := map[string]bool{}
	for _, name := range backendSecretNames {
		value := secrets[name]
		if name == "bootstrap-password" {
			if len(value) != 32 || !isSHA256(value+value) {
				return nil, errors.New("invalid synthetic bootstrap credential format")
			}
		} else if !isSHA256(value) || seen[value] {
			return nil, errors.New("invalid or reused synthetic service credential format")
		}
		seen[value] = true
	}
	result := map[string][]byte{}
	for name, value := range secrets {
		result[name] = []byte(value)
	}
	for _, name := range []string{"smtp-password", "proxy-password", "telegram-bot"} {
		value, err := backendShortSecret()
		if err != nil {
			return nil, err
		}
		if name == "telegram-bot" {
			value = "1:" + value
		}
		result[name] = []byte(value)
	}
	result["smtp-username"], result["proxy-username"] = []byte("umir-e2e"), []byte("umir-e2e")
	for _, name := range append(slices.Clone(backendServices), "smtp", "notification") {
		value, err := randomToken()
		if err != nil {
			return nil, err
		}
		result["fixture-"+name] = []byte(value)
	}
	if err := validateBackendServiceMaterial(result); err != nil {
		return nil, err
	}
	certificates, err := backendSyntheticCertificates()
	if err != nil {
		return nil, err
	}
	for name, data := range certificates {
		result[name] = data
	}
	return result, nil
}
func backendSyntheticCertificates() (map[string][]byte, error) {
	result := map[string][]byte{}
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial := func() (*big.Int, error) { return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)) }
	number, err := serial()
	if err != nil {
		return nil, err
	}
	ca := &x509.Certificate{SerialNumber: number, Subject: pkix.Name{CommonName: "Umir synthetic fixture CA"}, NotBefore: time.Date(1999, 1, 1, 0, 0, 0, 0, time.FixedZone("Europe/Moscow", 3*3600)), NotAfter: time.Date(2101, 1, 1, 0, 0, 0, 0, time.FixedZone("Europe/Moscow", 3*3600)), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	result["synthetic-ca.pem"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	for name, host := range map[string]string{"smtp": "smtp", "notification": "api.telegram.org"} {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		number, err := serial()
		if err != nil {
			return nil, err
		}
		cert := &x509.Certificate{SerialNumber: number, Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
		if err != nil {
			return nil, err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}
		result[name+"-tls-cert"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		result[name+"-tls-key"] = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	}
	return result, nil
}
func prepareBackendFixtures(temp, project string, input backendInputs, secrets map[string]string, origin string) (*backendRunFixtures, map[string]backendFileRef, error) {
	f := input.Fixtures
	if f == nil || f.Protocol != backendFixtureProtocol || f.BindingsSHA256 != backendBindingsSHA256 {
		return nil, nil, errors.New("fixture provisioning protocol or reviewed binding differs")
	}
	credentials, err := backendSyntheticCredentials(secrets)
	if err != nil {
		return nil, nil, err
	}
	bootstrap, err := json.Marshal(f.Bootstrap)
	if err != nil {
		return nil, nil, err
	}
	run := &backendRunFixtures{Protocol: f.Protocol, BindingsSHA256: f.BindingsSHA256, FixtureID: f.FixtureID, InitialInstant: f.InitialInstant, InitialGeneration: "1", HarnessPrivateRoot: filepath.Join(temp, "fixtures", "harness"), Owners: map[string]backendRunOwner{}, BootstrapBindingSHA256: backendDigest(bootstrap)}
	harnessRefs := backendConsumerRefs("harness")
	urls := backendURLs(input)
	delete(urls, "smtp")
	delete(urls, "notification")
	for _, name := range append(append(slices.Clone(backendServices), "smtp", "notification"), "harness") {
		root := filepath.Join(temp, "fixtures", name)
		for _, dir := range []string{root, filepath.Join(root, "secrets"), filepath.Join(root, "config"), filepath.Join(root, "private")} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, nil, err
			}
		}
		refs := backendConsumerRefs(name)
		for key := range refs {
			if err := os.WriteFile(filepath.Join(root, "secrets", key), credentials[key], 0o600); err != nil {
				return nil, nil, err
			}
		}
		if name == "harness" {
			continue
		}
		var config any
		if slices.Contains(backendServices, name) {
			s := input.Services[name]
			caOwner := name
			if name != "auth" && name != "feedback" {
				caOwner = "auth"
			}
			owner := backendOwnerConfig{Version: 1, Protocol: f.Protocol, Project: project, Owner: name, FixtureID: f.FixtureID, InitialInstant: f.InitialInstant, ClockGeneration: "1", ControlCredentialFile: refs["fixture-"+name], CredentialFiles: backendServiceRefs(refs), ControlSocket: backendControlSocket, PrivatePort: s.Port, PublicPort: s.PublicPort, Origin: origin, ServiceURLs: urls, Delivery: backendDeliveryConfig{"smtp", 2525, "starttls", backendCredentialRef(caOwner, "synthetic-ca.pem"), "http://notification:3128", backendCredentialRef(caOwner, "synthetic-ca.pem")}}
			if name == "auth" {
				owner.Bootstrap = &f.Bootstrap
				owner.StateFile = f.Bootstrap.StateFile
			}
			if name == "feedback" {
				owner.StateFile = "/var/lib/umir-feedback/feedback.jsonl"
			}
			config = owner
		} else {
			s := input.Sinks[name]
			config = backendSinkConfig{1, f.Protocol, project, name, s.ControlPort, s.Port, refs["fixture-"+name], backendServiceRefs(refs), 256, 65536, 16777216}
		}
		data, err := json.Marshal(config)
		if err != nil {
			return nil, nil, err
		}
		if err := os.WriteFile(filepath.Join(root, "config", "config.json"), data, 0o600); err != nil {
			return nil, nil, err
		}
		if slices.Contains(backendServices, name) {
			run.Owners[name] = backendRunOwner{Profile: f.Owners[name], ConfigSHA256: backendDigest(data), ConfigPath: backendFixtureConfigPath, ControlSocket: backendControlSocket, ControlCredentialFile: refs["fixture-"+name]}
		}
	}
	return run, harnessRefs, nil
}
func mountBackendFixtures(temp string, services map[string]any) error {
	for _, name := range append(slices.Clone(backendServices), "smtp", "notification") {
		service, ok := services[name].(map[string]any)
		if !ok {
			return errors.New("missing fixture service topology")
		}
		var volumes []any
		if raw, present := service["volumes"]; present {
			var ok bool
			volumes, ok = raw.([]any)
			if !ok {
				return errors.New("invalid fixture volume topology")
			}
		}
		for _, kind := range []string{"config", "secrets"} {
			host := filepath.Join(temp, "fixtures", name, kind)
			if err := validateBackendPrivateDirectory(host); err != nil {
				return err
			}
			volumes = append(volumes, map[string]any{"type": "bind", "source": host, "target": "/run/umir-fixtures/" + kind, "read_only": true, "bind": map[string]any{"create_host_path": false}})
		}
		service["volumes"] = volumes
		service["user"] = "65532:65532"
		service["read_only"] = true
		service["tmpfs"] = []string{"/run/umir-fixtures:rw,noexec,nosuid,nodev,uid=65532,gid=65532,mode=0700"}
		if name == "auth" || name == "feedback" {
			service["environment"] = map[string]string{"SSL_CERT_FILE": "/run/umir-fixtures/secrets/synthetic-ca.pem"}
		}
	}
	return nil
}
func validateBackendPrivateDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return errors.New("fixture private directory is missing or unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Gid != uint32(os.Getgid()) {
		return errors.New("fixture private directory ownership mismatch")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	base, baseErr := filepath.EvalSymlinks(filepath.Dir(dir))
	if err != nil || baseErr != nil || resolved != filepath.Join(base, filepath.Base(dir)) {
		return errors.New("fixture private directory symlink forbidden")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		info, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 16384 {
			return errors.New("fixture credential/config file is unsafe")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Getuid()) || stat.Gid != uint32(os.Getgid()) {
			return errors.New("fixture private file ownership mismatch")
		}
	}
	return nil
}
func backendComposeLiterals(values []string) []string {
	result := slices.Clone(values)
	for i, value := range result {
		result[i] = strings.ReplaceAll(value, "$", "$$")
	}
	return result
}
func backendPublishedPort(target int, published string) map[string]any {
	return map[string]any{"target": target, "published": published, "host_ip": "127.0.0.1", "protocol": "tcp"}
}
func backendComposeArgs(project, path string) []string {
	return []string{"compose", "--project-name", project, "--file", path}
}

type backendPublisher struct {
	URL           string `json:"URL"`
	TargetPort    int    `json:"TargetPort"`
	PublishedPort int    `json:"PublishedPort"`
	Protocol      string `json:"Protocol"`
}
type backendContainer struct {
	ID         string             `json:"ID"`
	Service    string             `json:"Service"`
	State      string             `json:"State"`
	Health     string             `json:"Health"`
	ImageID    string             `json:"imageID"`
	Publishers []backendPublisher `json:"Publishers"`
}

func backendContainers(ctx context.Context, dir, project, compose string, input backendInputs, images map[string]imageEvidence, executor commandExecutor, requireReady bool) ([]backendContainer, error) {
	out, err := readBackendOwnedCommand(ctx, dir, executor, append(backendComposeArgs(project, compose), "ps", "--all", "--format", "json")...)
	if err != nil {
		return nil, errors.New("backend container inventory unavailable")
	}
	var containers []backendContainer
	if strings.TrimSpace(string(out)) == "null" {
		return nil, errors.New("backend container inventory is null")
	}
	if err := json.Unmarshal(out, &containers); err != nil {
		containers = nil
		decoder := json.NewDecoder(strings.NewReader(string(out)))
		for {
			var c backendContainer
			if err := decoder.Decode(&c); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, errors.New("invalid backend container inventory")
			}
			containers = append(containers, c)
		}
	}
	seen := map[string]bool{}
	seenIDs := map[string]bool{}
	seenPorts := map[int]bool{}
	for i, c := range containers {
		s, service := input.Services[c.Service]
		sink, sinkOK := input.Sinks[c.Service]
		if c.ID == "" || seenIDs[c.ID] || seen[c.Service] || (!service && !sinkOK) {
			return nil, errors.New("unexpected or duplicate backend container")
		}
		seen[c.Service] = true
		seenIDs[c.ID] = true
		labelsOut, err := readBackendOwnedCommand(ctx, dir, executor, "inspect", "--format", "{{json .}}", c.ID)
		if err != nil {
			return nil, errors.New("backend container ownership unavailable")
		}
		var inspected struct {
			ID     string `json:"Id"`
			Image  string `json:"Image"`
			Config struct {
				Labels map[string]string `json:"Labels"`
			} `json:"Config"`
		}
		if err := json.Unmarshal(labelsOut, &inspected); err != nil || inspected.Config.Labels["com.docker.compose.project"] != project || inspected.Config.Labels["com.docker.compose.service"] != c.Service {
			return nil, errors.New("backend container is not owned by recorded project")
		}
		if input.Version == 2 && (inspected.ID != c.ID || inspected.Config.Labels["io.umir.backend.run"] != project) {
			return nil, errors.New("backend container run or exact identity differs")
		}
		containers[i].ImageID = inspected.Image
		if requireReady && (images[c.Service].ID == "" || inspected.Image != images[c.Service].ID) {
			return nil, errors.New("running backend image differs from reviewed build")
		}
		if requireReady && (c.State != "running" || service && c.Health != "healthy") {
			return nil, errors.New("backend service is not ready")
		}
		if !requireReady {
			continue
		}
		targets := map[int]bool{}
		if c.Service == "gateway" {
			targets[s.PublicPort] = false
		}
		if sinkOK {
			targets[sink.ControlPort] = false
		}
		if service && input.Version == 2 {
			targets[s.Port] = false
		}
		published := 0
		for _, p := range c.Publishers {
			if p.PublishedPort > 0 {
				published++
				already, allowed := targets[p.TargetPort]
				if !allowed || already || p.URL != "127.0.0.1" || p.Protocol != "tcp" || p.PublishedPort > 65535 || input.Version == 2 && seenPorts[p.PublishedPort] {
					return nil, errors.New("backend topology published an unauthorized port")
				}
				targets[p.TargetPort] = true
				seenPorts[p.PublishedPort] = true
			}
		}
		if published != len(targets) {
			return nil, errors.New("backend published port count is invalid")
		}
	}
	if requireReady && len(seen) != 7 {
		return nil, errors.New("backend topology is incomplete")
	}
	return containers, nil
}
func backendEndpoint(ctx context.Context, dir, project, compose, service string, port int, executor commandExecutor) (string, error) {
	args := append(backendComposeArgs(project, compose), "port", service, strconv.Itoa(port))
	out, err := readBackendOwnedCommand(ctx, dir, executor, args...)
	if err != nil {
		return "", errors.New("backend endpoint unavailable")
	}
	address := strings.TrimSpace(string(out))
	if len(out) > 128 || !validBackendPrivateURL("http://"+address) {
		return "", errors.New("backend endpoint is not loopback")
	}
	return "http://" + address, nil
}
func writeBackendJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
func cleanupBackendProject(ctx context.Context, dir, project, compose string, input backendInputs, executor commandExecutor) error {
	if _, err := backendContainers(ctx, dir, project, compose, input, nil, executor, false); err != nil {
		return err
	}
	if _, _, err := executor.Run(ctx, dir, commandEnv(), "docker", append(backendComposeArgs(project, compose), "down", "--volumes", "--remove-orphans", "--timeout", "10")...); err != nil {
		return errors.New("owned backend Compose cleanup failed")
	}
	for _, args := range [][]string{{"ps", "-aq"}, {"volume", "ls", "-q"}, {"network", "ls", "-q"}} {
		args = append(args, "--filter", "label=com.docker.compose.project="+project)
		out, _, err := executor.Run(ctx, dir, commandEnv(), "docker", args...)
		if err != nil {
			return errors.New("backend cleanup inventory failed")
		}
		if len(strings.TrimSpace(string(out))) > 0 {
			return fmt.Errorf("owned backend %s resources remain after cleanup", args[0])
		}
	}
	return nil
}
