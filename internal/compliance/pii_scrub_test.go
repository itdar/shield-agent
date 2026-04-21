package compliance

import (
	"strings"
	"testing"
)

func TestScrubKoreanRRN(t *testing.T) {
	s := NewPIIScrubber(RedactMask, nil)
	got, matched := s.Scrub("환자번호 900101-1234567 입니다")
	if strings.Contains(got, "900101-1234567") {
		t.Errorf("RRN leaked in output: %q", got)
	}
	if len(matched) == 0 {
		t.Error("pattern match not detected")
	}
	if !contains(matched, "kr_rrn") {
		t.Errorf("expected kr_rrn in matches, got %v", matched)
	}
}

func TestScrubEmail(t *testing.T) {
	s := NewPIIScrubber(RedactMask, nil)
	got, _ := s.Scrub("contact me at john.doe@example.com for details")
	if strings.Contains(got, "john.doe@example.com") {
		t.Errorf("email leaked: %q", got)
	}
}

func TestScrubHashMode(t *testing.T) {
	s := NewPIIScrubber(RedactHash, nil)
	got, _ := s.Scrub("email: a@b.co")
	if !strings.Contains(got, "sha256:") {
		t.Errorf("hash mode should produce sha256: prefix, got %q", got)
	}
}

func TestScrubCustomPattern(t *testing.T) {
	s := NewPIIScrubber(RedactMask, []string{`SECRET-\d+`})
	got, matched := s.Scrub("token SECRET-42 leaked")
	if strings.Contains(got, "SECRET-42") {
		t.Errorf("custom pattern not scrubbed: %q", got)
	}
	if !contains(matched, "custom_0") {
		t.Errorf("custom match not recorded: %v", matched)
	}
}

func TestScrubNoMatch(t *testing.T) {
	s := NewPIIScrubber(RedactMask, nil)
	input := "no PII here"
	got, matched := s.Scrub(input)
	if got != input {
		t.Errorf("unchanged input should be returned, got %q", got)
	}
	if len(matched) != 0 {
		t.Errorf("expected no matches, got %v", matched)
	}
}

func TestScrubURL(t *testing.T) {
	s := NewPIIScrubber(RedactMask, nil)
	got, matched := s.ScrubURL("/v1/users/jane@doe.com/history")
	if strings.Contains(got, "jane@doe.com") {
		t.Errorf("URL email leaked: %q", got)
	}
	if len(matched) == 0 {
		t.Error("URL email pattern not matched")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
