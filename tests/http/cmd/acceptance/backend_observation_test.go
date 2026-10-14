package main

import (
	"encoding/json"
	"os"
	"testing"
)

type feedbackExampleOptions struct {
	TrustedSubmissions []feedbackSubmission `json:"trustedSubmissions"`
	ExpectedInstanceID string               `json:"expectedInstanceID"`
	BeforeInstanceID   string               `json:"beforeInstanceID"`
	AfterInstanceID    string               `json:"afterInstanceID"`
	Mode               string               `json:"mode"`
}
type feedbackExample struct {
	Name                                       string
	Request, Response                          json.RawMessage
	BeforeRequest, Before, AfterRequest, After json.RawMessage
	Options                                    feedbackExampleOptions
}

func TestFeedbackObservationApprovedSemanticCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/feedback-observations.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct{ Positive, Negative, ReplayPositive, ReplayNegative []feedbackExample }
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, group := range []struct {
		cases          []feedbackExample
		valid, compare bool
	}{{corpus.Positive, true, false}, {corpus.Negative, false, false}, {corpus.ReplayPositive, true, true}, {corpus.ReplayNegative, false, true}} {
		for _, example := range group.cases {
			t.Run(example.Name, func(t *testing.T) {
				var err error
				if group.compare {
					err = compareFeedbackObservations(example.BeforeRequest, example.Before, example.AfterRequest, example.After, example.Options.TrustedSubmissions, example.Options.BeforeInstanceID, example.Options.AfterInstanceID, example.Options.Mode)
				} else {
					_, err = validateFeedbackObservation(example.Request, example.Response, example.Options.TrustedSubmissions, example.Options.ExpectedInstanceID)
				}
				if (err == nil) != group.valid {
					t.Fatalf("valid=%v error=%v", group.valid, err)
				}
			})
		}
	}
}

func TestFeedbackCanonicalJSONMatchesReferenceUnicodeEscaping(t *testing.T) {
	value := struct {
		CommandID string `json:"commandID"`
	}{"literal\\u2028;actual\u2028\u2029;<>&"}
	raw, err := canonicalFeedbackJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"commandID":"literal\\u2028;actual` + "\u2028\u2029" + `;<>&"}`
	if string(raw) != expected {
		t.Fatal("canonical bytes differ from JSON.stringify")
	}
}
