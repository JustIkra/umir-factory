package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const correctedBindingsDigest = "af215b378140b8e690523fc537007679b8c2599ed3039f7f2a0c651a64c59410"
const predecessorBindingsDigest = "de9c7bfc573ad0887be0f00b1ddc2495939469f58f3524c6dbb66a521fed385e"

var refusedBindingsDigests = []string{
	predecessorBindingsDigest,
	"fc7e04b922e704fe650ae25a1172d547fa64e58ec9971d97e90b3f71878bf163",
	"4660c4e0ec307b01849148065ea08e271997192d4c6298b574d6afa8c687bd38",
	strings.Repeat("f", 64),
}

func TestBackendBindingCorrectionGeneratedMaps(t *testing.T) {
	f := newBackendV2Fixture(t)
	o, err := parseOptions("http-acceptance", f.args)
	if err != nil {
		t.Fatal(err)
	}
	input, err := readBackendInputs(o)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := backendSecrets()
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	_, harness, err := prepareBackendFixtures(temp, "umir-backend-0123456789abcdef01234567", input, secrets, "http://127.0.0.1:49152")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range append(slices.Clone(backendServices), "smtp", "notification") {
		raw, err := os.ReadFile(filepath.Join(temp, "fixtures", target, "config", "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			CredentialFiles       map[string]backendFileRef `json:"credentialFiles"`
			ControlCredentialFile backendFileRef            `json:"controlCredentialFile"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for name, ref := range cfg.CredentialFiles {
			names = append(names, name)
			if strings.HasPrefix(name, "fixture-") {
				t.Errorf("%s control reference in service map", target)
			}
			if ref != backendCredentialRef(target, name) {
				t.Errorf("%s service reference has wrong target or path", target)
			}
		}
		slices.Sort(names)
		t.Logf("%s generated credential names: %v", target, names)
		if cfg.ControlCredentialFile != backendCredentialRef(target, "fixture-"+target) {
			t.Errorf("%s separate control reference differs", target)
		}
		if target == "serving" && !slices.Equal(names, []string{"auth-serving", "gateway-serving", "serving-auth", "serving-warehouse"}) {
			t.Error("Serving service map must have exactly four reviewed keys")
		}
	}
	for _, target := range append(append(slices.Clone(backendServices), "smtp", "notification"), "harness") {
		data, err := os.ReadFile(filepath.Join(temp, "fixtures", target, "secrets", "serving-auth"))
		allowed := target == "serving" || target == "auth" || target == "harness"
		if allowed {
			if err != nil || !isSHA256(string(data)) {
				t.Errorf("%s discovery credential missing or malformed", target)
				continue
			}
			if string(data) != secrets["serving-auth"] || string(data) == secrets["auth-serving"] {
				t.Errorf("%s discovery credential reused or differs from generated source", target)
			}
		} else if !os.IsNotExist(err) {
			t.Errorf("discovery credential delivered to %s", target)
		}
	}
	if harness["serving-auth"] != backendCredentialRef("harness", "serving-auth") {
		t.Error("harness discovery reference missing or wrong")
	}
}

func TestBackendBindingCorrectionReviewedDigest(t *testing.T) {
	for _, digest := range append([]string{correctedBindingsDigest}, refusedBindingsDigests...) {
		t.Run(digest[:8], func(t *testing.T) {
			f := newBackendV2Fixture(t)
			backendValue[map[string]any](t, f.document["fixtures"])["bindingsSHA256"] = digest
			f.refresh(t)
			o, err := parseOptions("http-acceptance", f.args)
			if err != nil {
				t.Fatal(err)
			}
			input, err := readBackendInputs(o)
			if digest != correctedBindingsDigest {
				if err == nil {
					t.Fatal("stale or wrong reviewed bindings digest accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("new reviewed bindings digest refused: %v", err)
			}
			secrets, err := backendSecrets()
			if err != nil {
				t.Fatal(err)
			}
			run, _, err := prepareBackendFixtures(t.TempDir(), "umir-backend-0123456789abcdef01234567", input, secrets, "http://127.0.0.1:49152")
			if err != nil {
				t.Fatal(err)
			}
			if run.BindingsSHA256 != correctedBindingsDigest || run.Protocol != "umir-operational-fixtures/1.1.0" {
				t.Fatal("run digest or protocol differs")
			}
		})
	}
}

func TestBackendBindingCorrectionRejectsReusedServiceMaterial(t *testing.T) {
	secrets, err := backendSecrets()
	if err != nil {
		t.Fatal(err)
	}
	secrets["auth-serving"] = secrets["gateway-serving"]
	if _, err := backendSyntheticCredentials(secrets); err == nil {
		t.Fatal("reused service credential accepted")
	}
}

func TestBackendBindingCorrectionDistinctRandomMaterial(t *testing.T) {
	seen := map[string]bool{}
	for range 2 {
		secrets, err := backendSecrets()
		if err != nil {
			t.Fatal(err)
		}
		material, err := backendSyntheticCredentials(secrets)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range append(slices.Clone(backendSecretNames), "fixture-auth", "fixture-feedback", "fixture-warehouse", "fixture-serving", "fixture-gateway", "fixture-smtp", "fixture-notification") {
			if name == "bootstrap-password" {
				continue
			}
			value := string(material[name])
			if !isSHA256(value) || seen[value] {
				t.Fatalf("%s material malformed or reused", name)
			}
			seen[value] = true
		}
		if !isSHA256(string(material["serving-auth"])) {
			t.Error("distinct random discovery credential absent")
		}
	}
}

func reviewedBackendCredentialRefs(t *testing.T, target string) map[string]backendFileRef {
	t.Helper()
	data, err := os.ReadFile("testdata/binding-correction.json")
	if err != nil {
		t.Fatal(err)
	}
	var reviewed struct {
		Protocol          string              `json:"protocol"`
		BindingsSHA256    string              `json:"bindingsSHA256"`
		CredentialTargets map[string][]string `json:"credentialTargets"`
	}
	if err := json.Unmarshal(data, &reviewed); err != nil {
		t.Fatal(err)
	}
	if reviewed.Protocol != backendFixtureProtocol || reviewed.BindingsSHA256 != backendBindingsSHA256 {
		t.Fatal("reviewed provisioning identity differs")
	}
	refs := map[string]backendFileRef{}
	for name, targets := range reviewed.CredentialTargets {
		allowed := slices.Contains(targets, target)
		if target == "harness" {
			parts := strings.Split(name, "-")
			servicePair := len(parts) == 2 && slices.Contains(backendServices, parts[0]) && slices.Contains(backendServices, parts[1])
			allowed = servicePair || name == "bootstrap-password" || strings.HasPrefix(name, "fixture-")
		}
		if allowed {
			refs[name] = backendFileRef{Target: target, Path: "/run/umir-fixtures/secrets/" + name}
		}
	}
	return refs
}

func TestBackendBindingCorrectionExactReviewedTargets(t *testing.T) {
	for _, target := range append(append(slices.Clone(backendServices), "smtp", "notification"), "harness") {
		expected := reviewedBackendCredentialRefs(t, target)
		actual := backendConsumerRefs(target)
		if len(actual) != len(expected) {
			t.Fatalf("%s consumer inventory differs from reviewed targets", target)
		}
		for name, ref := range expected {
			if actual[name] != ref {
				t.Fatalf("%s/%s reference missing, aliased or wrong-target", target, name)
			}
		}
	}
}

func TestBackendBindingCorrectionRejectsBadServiceControlMaterial(t *testing.T) {
	for _, name := range []string{"serving-auth", "auth-serving", "fixture-serving", "fixture-auth"} {
		for _, kind := range []string{"missing", "empty", "nonhex", "uppercase", "short", "long", "whitespace", "reused-service", "reused-control"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				secrets, err := backendSecrets()
				if err != nil {
					t.Fatal(err)
				}
				material, err := backendSyntheticCredentials(secrets)
				if err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "missing":
					delete(material, name)
				case "empty":
					material[name] = nil
				case "nonhex":
					material[name] = []byte(strings.Repeat("z", 64))
				case "uppercase":
					material[name] = []byte(strings.Repeat("A", 64))
				case "short":
					material[name] = []byte(strings.Repeat("a", 63))
				case "long":
					material[name] = []byte(strings.Repeat("a", 65))
				case "whitespace":
					material[name] = []byte(strings.Repeat("a", 63) + "\n")
				case "reused-service":
					material[name] = slices.Clone(material["gateway-serving"])
				case "reused-control":
					material[name] = slices.Clone(material["fixture-gateway"])
				}
				if err := validateBackendServiceMaterial(material); err == nil {
					t.Fatal("invalid or reused service/control credential accepted")
				}
			})
		}
	}
}

func TestBackendBindingCorrectionServiceMapKeepsPrivateInventory(t *testing.T) {
	refs := backendConsumerRefs("serving")
	services := backendServiceRefs(refs)
	if refs["fixture-serving"] != backendCredentialRef("serving", "fixture-serving") {
		t.Fatal("private control inventory lost")
	}
	if _, ok := services["fixture-serving"]; ok {
		t.Fatal("control included in service map")
	}
	delete(services, "serving-auth")
	if refs["serving-auth"] != backendCredentialRef("serving", "serving-auth") {
		t.Fatal("service map aliases private inventory")
	}
}

func TestBackendBindingCorrectionProvisioningRefusesOldDigest(t *testing.T) {
	f := newBackendV2Fixture(t)
	o, err := parseOptions("http-acceptance", f.args)
	if err != nil {
		t.Fatal(err)
	}
	input, err := readBackendInputs(o)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := backendSecrets()
	if err != nil {
		t.Fatal(err)
	}
	for _, digest := range refusedBindingsDigests {
		t.Run(digest[:8], func(t *testing.T) {
			input.Fixtures.BindingsSHA256 = digest
			if _, _, err := prepareBackendFixtures(t.TempDir(), "umir-backend-0123456789abcdef01234567", input, secrets, "http://127.0.0.1:49152"); err == nil {
				t.Fatal("provisioning accepts stale or wrong binding identity")
			}
		})
	}
}

func TestBackendBindingCorrectionReadinessRefusesOldDigest(t *testing.T) {
	f := newBackendV2Fixture(t)
	o, err := parseOptions("http-acceptance", f.args)
	if err != nil {
		t.Fatal(err)
	}
	input, err := readBackendInputs(o)
	if err != nil {
		t.Fatal(err)
	}
	for _, digest := range refusedBindingsDigests {
		for _, boundary := range []string{"input", "run"} {
			t.Run(digest[:8]+"/"+boundary, func(t *testing.T) {
				input.Fixtures.BindingsSHA256 = correctedBindingsDigest
				fixtures := &backendRunFixtures{Protocol: backendFixtureProtocol, BindingsSHA256: correctedBindingsDigest}
				if boundary == "input" {
					input.Fixtures.BindingsSHA256 = digest
				} else {
					fixtures.BindingsSHA256 = digest
				}
				err := probeBackendOwners(context.Background(), "", "", "umir-backend-0123456789abcdef01234567", "", input, nil, fixtures, nil, &backendExecutor{})
				if err == nil || !strings.Contains(err.Error(), "binding") {
					t.Fatal("readiness does not refuse stale or wrong binding identity before external operations")
				}
			})
		}
	}
}
