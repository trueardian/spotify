package firestore

import (
	"strings"
	"testing"
)

func TestValidateUserID(t *testing.T) {
	valid := []string{
		"user-123",
		"someone@example.com",
		"a",
		strings.Repeat("x", 1500),
		"__x", "x__", "___", // too short to match the reserved __*__ pattern
		".hidden", "..dots",
	}
	for _, id := range valid {
		if err := validateUserID(id); err != nil {
			t.Errorf("validateUserID(%q): unexpected error: %v", id, err)
		}
	}

	invalid := []string{
		"",
		".", "..",
		"tenants/alice",
		strings.Repeat("x", 1501),
		"__reserved__", "____",
	}
	for _, id := range invalid {
		if err := validateUserID(id); err == nil {
			t.Errorf("validateUserID(%q): want error, got nil", id)
		}
	}
}
