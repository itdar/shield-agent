package compliance

import (
	"encoding/json"
)

// ContentTag is the triple we attach to every MITM-seen response: which
// AI model produced it, how we'd classify the content, and whether we
// should inject an X-AI-Generated: true hint when replaying back to the
// client.
type ContentTag struct {
	Provider     string
	Model        string
	ContentClass string // "ai_generated" | "human" | "mixed" | ""
	AIGenerated  bool
}

// Tagger extracts a ContentTag from a provider response. It understands
// the JSON shape of the major LLM APIs today (OpenAI, Anthropic, Google
// Gemini). When the response shape is unrecognised the tag comes back
// with only the provider populated — this is deliberately non-fatal so
// unknown providers still get logged with something meaningful.
type Tagger struct {
	providers map[string]providerExtractor
}

type providerExtractor func(body []byte) (model string, ok bool)

// NewTagger returns a tagger with the builtin rules wired up.
func NewTagger() *Tagger {
	return &Tagger{
		providers: map[string]providerExtractor{
			"openai":    openaiExtractor,
			"anthropic": anthropicExtractor,
			"google":    googleExtractor,
		},
	}
}

// Tag classifies the response for the given provider. An empty or
// "unknown" providerName triggers a best-effort probe against every
// registered extractor — if any one pulls a model name, we still get
// an ai_generated tag. This matters for Phase 2 deployments against
// providers we don't know the hostname of (e.g. private Azure OpenAI).
func (t *Tagger) Tag(providerName string, body []byte) ContentTag {
	tag := ContentTag{Provider: providerName}
	if len(body) == 0 {
		return tag
	}
	if providerName != "" && providerName != "unknown" {
		if extractor, ok := t.providers[providerName]; ok {
			if model, found := extractor(body); found {
				tag.Model = model
				tag.ContentClass = "ai_generated"
				tag.AIGenerated = true
			}
			return tag
		}
	}
	// Fallback probe.
	for _, extractor := range t.providers {
		if model, found := extractor(body); found {
			tag.Model = model
			tag.ContentClass = "ai_generated"
			tag.AIGenerated = true
			return tag
		}
	}
	return tag
}

// --- provider-specific extractors -------------------------------------------
// Each reads the top-level `model` field. The structs are intentionally
// permissive (extra fields ignored) so we survive the providers tacking
// new metadata onto their responses.

func openaiExtractor(body []byte) (string, bool) {
	var v struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", false
	}
	return v.Model, v.Model != ""
}

func anthropicExtractor(body []byte) (string, bool) {
	var v struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", false
	}
	return v.Model, v.Model != ""
}

func googleExtractor(body []byte) (string, bool) {
	// Gemini responses sometimes nest the model name under `modelVersion`
	// or under `candidates[0].content.parts[0].inlineData`. We prefer
	// the flat field when present and fall back to modelVersion.
	var v struct {
		Model        string `json:"model"`
		ModelVersion string `json:"modelVersion"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", false
	}
	if v.Model != "" {
		return v.Model, true
	}
	if v.ModelVersion != "" {
		return v.ModelVersion, true
	}
	return "", false
}
