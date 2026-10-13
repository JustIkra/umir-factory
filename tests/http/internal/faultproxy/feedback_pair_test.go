package faultproxy

import "testing"

func TestFeedbackObservationRequiresExactNamespacePairs(t *testing.T) {
	prefix := `{"version":1,"project":"umir-backend-0123456789abcdef01234567","requestID":"11111111-1111-4111-8111-111111111111","operation":"feedback.observe","data":`
	pair := prefix + `{"submissions":[{"namespace":{"kind":"anonymous"},"submissionID":"22222222-2222-4222-8222-222222222222"}],"waitMs":0}}`
	legacy := prefix + `{"submissionIDs":["22222222-2222-4222-8222-222222222222"],"waitMs":0}}`
	t.Run("new pair selector", func(t *testing.T) {
		if err := ValidateOperational("#/$defs/feedback_observe_request", []byte(pair)); err != nil {
			t.Fatal("exact pair selector rejected")
		}
	})
	t.Run("legacy selector", func(t *testing.T) {
		if err := ValidateOperational("#/$defs/feedback_observe_request", []byte(legacy)); err == nil {
			t.Fatal("ambiguous ID-only selector accepted")
		}
	})
}
