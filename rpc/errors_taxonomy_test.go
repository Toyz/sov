package rpc

import "testing"

func TestError_RetryableAndDetailsRoundTrip(t *testing.T) {
	e := BadRequest("bad").
		WithDetails(FieldError{Field: "age", Code: "MIN", Message: "must be >= 0"}).
		Retry(5)
	if !e.Retryable || e.RetryAfter != 5 || len(e.Details) != 1 {
		t.Fatalf("builder: %+v", e)
	}
	got, ok := DecodeErrorBody(MarshalError(e), 400)
	if !ok {
		t.Fatal("decode failed")
	}
	if !got.Retryable || got.RetryAfter != 5 {
		t.Fatalf("retry did not round-trip: %+v", got)
	}
	if len(got.Details) != 1 || got.Details[0].Field != "age" || got.Details[0].Code != "MIN" {
		t.Fatalf("details did not round-trip: %+v", got.Details)
	}
}

func TestTooManyRequests_IsRetryable(t *testing.T) {
	if !TooManyRequests("slow down").Retryable {
		t.Fatal("429 RATE_LIMITED should be marked retryable")
	}
}
