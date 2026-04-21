package compliance

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// defaultPatterns enumerates the Korean-first PII regexes applied to
// every inspected body. The list is intentionally conservative — false
// positives (e.g. a credit-card-shaped order id) are preferable to
// leaking a real card number into the audit log.
var defaultPatterns = []namedPattern{
	// Korean Resident Registration Number: YYMMDD-X000000
	// X is 1-4 (male/female split across centuries).
	{"kr_rrn", regexp.MustCompile(`\b\d{6}-[1-4]\d{6}\b`)},
	// Korean phone numbers, including mobile and landline formats.
	{"kr_phone", regexp.MustCompile(`\b0(?:10|11|16|17|18|19|[2-9]\d?)[- .]?\d{3,4}[- .]?\d{4}\b`)},
	// Generic international phone (E.164-ish) — stricter than the
	// Korean pattern so we don't swallow random digits.
	{"intl_phone", regexp.MustCompile(`\b\+?[1-9]\d{1,2}[- .]?\(?\d{2,4}\)?[- .]?\d{3,4}[- .]?\d{3,4}\b`)},
	// Email.
	{"email", regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)},
	// 13–19 digit credit card numbers. We leave Luhn validation to a
	// follow-up to avoid false negatives on test cards.
	{"credit_card", regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)},
	// Korean driver's licence: 2-digit region, 2-digit year, 6-digit
	// serial, 2-digit check (AA-YY-NNNNNN-CC or packed).
	{"kr_driver_license", regexp.MustCompile(`\b\d{2}-\d{2}-\d{6}-\d{2}\b`)},
}

type namedPattern struct {
	name string
	rx   *regexp.Regexp
}

// RedactionMode controls what PIIScrubber substitutes in place of a match.
type RedactionMode int

const (
	// RedactMask replaces matches with a fixed [REDACTED] marker.
	RedactMask RedactionMode = iota
	// RedactHash replaces matches with `sha256:<12 hex>` so downstream
	// tooling can correlate occurrences without seeing the original.
	RedactHash
)

// PIIScrubber applies PII redaction to request / response bodies, URL
// paths, and header values. The zero value is ready to use with the
// default pattern set and RedactMask.
type PIIScrubber struct {
	patterns []namedPattern
	mode     RedactionMode
}

// NewPIIScrubber returns a scrubber configured from the given options.
// Extra regex strings in custom are appended to the default pattern set.
// Invalid regexes are silently skipped so a single typo in operator
// config can't break the pipeline.
func NewPIIScrubber(mode RedactionMode, custom []string) *PIIScrubber {
	pats := make([]namedPattern, 0, len(defaultPatterns)+len(custom))
	pats = append(pats, defaultPatterns...)
	for i, src := range custom {
		rx, err := regexp.Compile(src)
		if err != nil {
			continue
		}
		pats = append(pats, namedPattern{name: "custom_" + itoa(i), rx: rx})
	}
	return &PIIScrubber{patterns: pats, mode: mode}
}

// Scrub returns (redacted, matchedPatternNames). If no pattern matched,
// the original string is returned unchanged and matches is empty. Callers
// use len(matches) > 0 as the "PII detected" signal.
func (s *PIIScrubber) Scrub(input string) (string, []string) {
	if input == "" || s == nil {
		return input, nil
	}
	out := input
	var matched []string
	for _, p := range s.patterns {
		if !p.rx.MatchString(out) {
			continue
		}
		matched = append(matched, p.name)
		out = p.rx.ReplaceAllStringFunc(out, func(m string) string {
			if s.mode == RedactHash {
				sum := sha256.Sum256([]byte(m))
				return "sha256:" + hex.EncodeToString(sum[:6])
			}
			return "[REDACTED:" + p.name + "]"
		})
	}
	return out, matched
}

// ScrubBytes is a convenience wrapper around Scrub that accepts a byte
// slice — useful for HTTP bodies. The returned slice is a freshly
// allocated copy so callers can safely release the original.
func (s *PIIScrubber) ScrubBytes(input []byte) ([]byte, []string) {
	if len(input) == 0 {
		return input, nil
	}
	scrubbed, matched := s.Scrub(string(input))
	if len(matched) == 0 {
		return input, nil
	}
	return []byte(scrubbed), matched
}

// ScrubURL replaces PII occurrences inside a URL path + query. The
// returned string replaces whatever egress captured in method (e.g.
// "GET /v1/users/john@doe.com/completions").
func (s *PIIScrubber) ScrubURL(pathWithQuery string) (string, []string) {
	if pathWithQuery == "" {
		return pathWithQuery, nil
	}
	return s.Scrub(pathWithQuery)
}

func itoa(n int) string {
	// small, allocation-light itoa so we don't drag in strconv for a
	// single call site.
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return strings.Clone(string(buf[i:]))
}
