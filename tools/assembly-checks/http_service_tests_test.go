package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateWarehouseServiceEvidenceRequiresThreeExactPassingTests(t *testing.T) {
	valid := strings.Join([]string{
		testEvent("run", "github.com/JustIkra/umir-warehouse/internal/application", "TestOrderAttemptDuplicateDuringOwnerExecutionReturnsPending"),
		testEvent("pass", "github.com/JustIkra/umir-warehouse/internal/application", "TestOrderAttemptDuplicateDuringOwnerExecutionReturnsPending"),
		testEvent("run", "github.com/JustIkra/umir-warehouse/internal/application", "TestOrderAttemptCachesRecheckAndRejectsCrossProposalReuse"),
		testEvent("pass", "github.com/JustIkra/umir-warehouse/internal/application", "TestOrderAttemptCachesRecheckAndRejectsCrossProposalReuse"),
		testEvent("run", "github.com/JustIkra/umir-warehouse/internal/adapters/httpapi", "TestOrderCommitPendingUsesHTTP202AndTaggedPayloadOnly"),
		testEvent("pass", "github.com/JustIkra/umir-warehouse/internal/adapters/httpapi", "TestOrderCommitPendingUsesHTTP202AndTaggedPayloadOnly"),
	}, "\n")
	if err := validateWarehouseServiceTestJSON([]byte(valid)); err != nil {
		t.Fatalf("complete required test evidence rejected: %v", err)
	}

	for name, output := range map[string]string{
		"missing test":  valid[:strings.LastIndex(valid, "\n")],
		"skipped test":  strings.Replace(valid, testEvent("pass", "github.com/JustIkra/umir-warehouse/internal/adapters/httpapi", "TestOrderCommitPendingUsesHTTP202AndTaggedPayloadOnly"), testEvent("skip", "github.com/JustIkra/umir-warehouse/internal/adapters/httpapi", "TestOrderCommitPendingUsesHTTP202AndTaggedPayloadOnly"), 1),
		"wrong package": strings.Replace(valid, "github.com/JustIkra/umir-warehouse/internal/application", "github.com/JustIkra/umir-warehouse/internal/domain", 2),
		"failed test":   strings.Replace(valid, testEvent("pass", "github.com/JustIkra/umir-warehouse/internal/application", "TestOrderAttemptDuplicateDuringOwnerExecutionReturnsPending"), testEvent("fail", "github.com/JustIkra/umir-warehouse/internal/application", "TestOrderAttemptDuplicateDuringOwnerExecutionReturnsPending"), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateWarehouseServiceTestJSON([]byte(output)); err == nil {
				t.Fatal("invalid service test evidence was accepted")
			}
		})
	}
}

func testEvent(action, packagePath, testName string) string {
	return fmt.Sprintf(`{"Action":%q,"Package":%q,"Test":%q}`, action, packagePath, testName)
}
