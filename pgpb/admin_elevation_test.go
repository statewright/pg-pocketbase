package pgpb

import (
	"testing"
)

func TestParseAdminEmails(t *testing.T) {
	cases := []struct {
		input    string
		expected []string
	}{
		{"", nil},
		{"  ", nil},
		{"ben@example.com", []string{"ben@example.com"}},
		{"ben@example.com,ops@company.dev", []string{"ben@example.com", "ops@company.dev"}},
		{" ben@example.com , ops@company.dev ", []string{"ben@example.com", "ops@company.dev"}},
		{"BEN@Example.com", []string{"ben@example.com"}},
		{"a@b.com,,c@d.com", []string{"a@b.com", "c@d.com"}},
	}

	for _, tc := range cases {
		result := parseAdminEmails(tc.input)
		if len(result) != len(tc.expected) {
			t.Fatalf("parseAdminEmails(%q): got %v, want %v", tc.input, result, tc.expected)
		}
		for i := range result {
			if result[i] != tc.expected[i] {
				t.Fatalf("parseAdminEmails(%q)[%d]: got %q, want %q", tc.input, i, result[i], tc.expected[i])
			}
		}
	}
}

func TestIsAllowed(t *testing.T) {
	allowlist := []string{"ben@example.com", "ops@company.dev"}

	if !isAllowed("ben@example.com", allowlist) {
		t.Fatal("should be allowed")
	}
	if !isAllowed("BEN@Example.com", allowlist) {
		t.Fatal("should be case-insensitive")
	}
	if isAllowed("nobody@example.com", allowlist) {
		t.Fatal("should not be allowed")
	}
	if isAllowed("", allowlist) {
		t.Fatal("empty email should not be allowed")
	}
	if isAllowed("ben@example.com", nil) {
		t.Fatal("empty allowlist should deny all")
	}
}

func TestSuperuserEmail(t *testing.T) {
	email := superuserEmail("abc123")
	if email != "pgpb_abc123@internal" {
		t.Fatalf("expected pgpb_abc123@internal, got %s", email)
	}
}
