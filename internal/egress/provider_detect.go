package egress

import "strings"

// ProviderOpenAI and friends name the well-known AI providers shield-agent
// can identify by their public API hostnames. Anything not matched is
// "unknown" — the caller should log but not filter based on that.
const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderGoogle    = "google"
	ProviderCohere    = "cohere"
	ProviderMistral   = "mistral"
	ProviderAzureAI   = "azure"
	ProviderUnknown   = "unknown"
)

// hostRules is the static table of suffix matches. Order matters for
// suffix overlap (none currently overlap, but keep the match loop
// deterministic).
var hostRules = []struct {
	suffix   string
	provider string
}{
	{"api.openai.com", ProviderOpenAI},
	{"api.anthropic.com", ProviderAnthropic},
	{"generativelanguage.googleapis.com", ProviderGoogle},
	{"aiplatform.googleapis.com", ProviderGoogle},
	{"api.cohere.ai", ProviderCohere},
	{"api.cohere.com", ProviderCohere},
	{"api.mistral.ai", ProviderMistral},
	{".openai.azure.com", ProviderAzureAI},
}

// StaticProviderDetector matches by hostname suffix against a fixed rule
// table. It is safe for concurrent use (read-only).
type StaticProviderDetector struct{}

// Detect returns the provider string for the given host, or
// ProviderUnknown when no rule matches.
func (StaticProviderDetector) Detect(host string) string {
	host = strings.ToLower(host)
	// Strip trailing dot (fully-qualified form) so rules still match.
	host = strings.TrimSuffix(host, ".")
	for _, r := range hostRules {
		// A bare suffix string matches the host itself OR any subdomain
		// (foo.example.com). We deliberately do NOT use HasSuffix(host,
		// r.suffix) alone — "notapi.openai.com" must not match
		// "api.openai.com".
		if strings.HasPrefix(r.suffix, ".") {
			if strings.HasSuffix(host, r.suffix) {
				return r.provider
			}
			continue
		}
		if host == r.suffix || strings.HasSuffix(host, "."+r.suffix) {
			return r.provider
		}
	}
	return ProviderUnknown
}

// DefaultProviderDetector returns the built-in detector.
func DefaultProviderDetector() ProviderDetector {
	return StaticProviderDetector{}
}
