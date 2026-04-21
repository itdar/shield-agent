package compliance

import "testing"

func TestTagOpenAIResponse(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-123","model":"gpt-4o","object":"chat.completion"}`)
	tag := NewTagger().Tag("openai", body)
	if tag.Model != "gpt-4o" {
		t.Errorf("Model = %q, want gpt-4o", tag.Model)
	}
	if !tag.AIGenerated {
		t.Error("AIGenerated should be true")
	}
	if tag.ContentClass != "ai_generated" {
		t.Errorf("ContentClass = %q, want ai_generated", tag.ContentClass)
	}
}

func TestTagAnthropicResponse(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","model":"claude-sonnet-4-20250514"}`)
	tag := NewTagger().Tag("anthropic", body)
	if tag.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q", tag.Model)
	}
	if !tag.AIGenerated {
		t.Error("should be AI generated")
	}
}

func TestTagGoogleModelVersionFallback(t *testing.T) {
	body := []byte(`{"modelVersion":"gemini-2.0-flash"}`)
	tag := NewTagger().Tag("google", body)
	if tag.Model != "gemini-2.0-flash" {
		t.Errorf("Model = %q, want gemini-2.0-flash", tag.Model)
	}
}

func TestTagUnknownProviderFallback(t *testing.T) {
	// When we don't know the provider but the body still looks like an
	// LLM response (top-level model field), the fallback probe picks it
	// up. This is the Phase 2 "best-effort tagging" behaviour — better
	// than silently dropping the compliance data for private LLMs.
	tag := NewTagger().Tag("unknown", []byte(`{"model":"foo"}`))
	if tag.Provider != "unknown" {
		t.Errorf("Provider = %q", tag.Provider)
	}
	if tag.Model != "foo" {
		t.Errorf("fallback probe should extract model, got %q", tag.Model)
	}
	if !tag.AIGenerated {
		t.Error("fallback probe should mark AI-generated when a model is found")
	}
}

func TestTagUnknownProviderNoModelField(t *testing.T) {
	// When there's genuinely no model field, the tag stays empty.
	tag := NewTagger().Tag("unknown", []byte(`{"greeting":"hi"}`))
	if tag.Model != "" {
		t.Errorf("Model = %q, want empty", tag.Model)
	}
	if tag.AIGenerated {
		t.Error("should not flag AI-generated without a model field")
	}
}

func TestTagMalformedJSON(t *testing.T) {
	tag := NewTagger().Tag("openai", []byte(`not json`))
	if tag.Model != "" {
		t.Errorf("malformed body should not produce model, got %q", tag.Model)
	}
}
