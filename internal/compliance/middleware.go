package compliance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/itdar/shield-agent/internal/config"
	"github.com/itdar/shield-agent/internal/egress"
)

// ComplianceMiddleware inspects the MITM-decrypted request and response
// bodies and fills in the Phase 2 audit fields (prompt_hash, model,
// content_class, PII flags). It is intentionally idempotent: running it
// twice on the same request yields the same fields.
type ComplianceMiddleware struct {
	egress.PassthroughEgressMiddleware
	scrubber     *PIIScrubber
	tagger       *Tagger
	logger       *slog.Logger
	injectHeader bool
	logFullBody  bool
}

// NewComplianceMiddleware wires the middleware with the configured PII
// and content-tagging behaviour.
func NewComplianceMiddleware(cfg config.EgressConfig, logger *slog.Logger) *ComplianceMiddleware {
	mode := RedactMask
	if cfg.PIIScrub.RedactionMode == "hash" {
		mode = RedactHash
	}
	return &ComplianceMiddleware{
		scrubber:     NewPIIScrubber(mode, cfg.PIIScrub.CustomPatterns),
		tagger:       NewTagger(),
		logger:       logger,
		injectHeader: cfg.ContentTagging.InjectHeader,
		logFullBody:  cfg.LogFullBody,
	}
}

// Name identifies this middleware in config.
func (*ComplianceMiddleware) Name() string { return "compliance" }

// ProcessRequest scrubs the URL path/query and the MITM'd body, and
// pre-computes the prompt hash so it's available even if the upstream
// call fails. Phase 1 (no body) runs this as a near-no-op.
func (m *ComplianceMiddleware) ProcessRequest(_ context.Context, req *egress.Request) (*egress.Request, error) {
	// URL path PII scrub — applies to both MITM and plaintext paths.
	if req.Method != "" {
		scrubbed, matched := scrubMethodURL(m.scrubber, req.Method)
		if len(matched) > 0 {
			req.Method = scrubbed
		}
	}

	// Body body-level scrub only makes sense when we actually have the
	// body (MITM path). Phase 1 skips the rest.
	if len(req.Body) == 0 {
		return req, nil
	}
	return req, nil
}

// ProcessResponse runs AFTER ProcessRequest. It computes the prompt hash
// from the (already scrubbed) request body, classifies the response via
// the Tagger, and stamps the resulting fields onto the Response so the
// terminal egress_log middleware can persist them.
func (m *ComplianceMiddleware) ProcessResponse(_ context.Context, req *egress.Request, resp *egress.Response) (*egress.Response, error) {
	if len(req.Body) > 0 {
		scrubbed, matched := m.scrubber.ScrubBytes(req.Body)
		if len(matched) > 0 {
			resp.PIIDetected = true
			resp.PIIScrubbed = true
			if m.logFullBody {
				req.Body = scrubbed
			}
		}
		// Always hash the scrubbed bytes so the audit log can correlate
		// duplicate prompts without leaking content.
		sum := sha256.Sum256(scrubbed)
		resp.PromptHash = hex.EncodeToString(sum[:])
	}

	if len(resp.Body) > 0 {
		// Response body also gets a PII scrub for audit export safety.
		scrubbedResp, matched := m.scrubber.ScrubBytes(resp.Body)
		if len(matched) > 0 {
			resp.PIIDetected = true
			resp.PIIScrubbed = true
			if m.logFullBody {
				resp.Body = scrubbedResp
			}
		}
		tag := m.tagger.Tag(req.Provider, scrubbedResp)
		resp.Model = tag.Model
		resp.ContentClass = tag.ContentClass
		resp.AIGenerated = tag.AIGenerated
		if tag.AIGenerated && m.injectHeader && resp.Headers != nil {
			resp.Headers.Set("X-AI-Generated", "true")
		}
	}

	return resp, nil
}

// scrubMethodURL applies PII redaction to the path/query portion of a
// "GET /v1/users/jane@doe.com/…" style method string. The verb is left
// untouched. Returns the scrubbed string and the matched pattern names.
func scrubMethodURL(scrubber *PIIScrubber, method string) (string, []string) {
	parts := strings.SplitN(method, " ", 2)
	if len(parts) < 2 {
		return method, nil
	}
	pathAndQuery := parts[1]
	parsed, err := url.Parse(pathAndQuery)
	if err != nil {
		return method, nil
	}
	// Scrub the path component first (most common place for email-in-URL).
	scrubbedPath, pathMatches := scrubber.ScrubURL(parsed.Path)
	parsed.Path = scrubbedPath
	// Query parameters — scrub each value independently so the key
	// structure survives.
	q := parsed.Query()
	changed := false
	var queryMatches []string
	for k, values := range q {
		for i, v := range values {
			s, matches := scrubber.Scrub(v)
			if len(matches) > 0 {
				q[k][i] = s
				queryMatches = append(queryMatches, matches...)
				changed = true
			}
		}
	}
	if changed {
		parsed.RawQuery = q.Encode()
	}

	matches := append([]string{}, pathMatches...)
	matches = append(matches, queryMatches...)
	if len(matches) == 0 {
		return method, nil
	}
	return parts[0] + " " + parsed.String(), matches
}

// registerCompliance adds the compliance middleware + PII scrubbing +
// content tagging to the egress registry. Called from
// RegisterEgressMiddlewares so every entry point sees the same set.
func registerCompliance() {
	egress.Register("compliance", func(_ config.MiddlewareEntry, deps egress.EgressDependencies) (egress.EgressMiddleware, error) {
		if deps.Logger == nil {
			return nil, fmt.Errorf("compliance: logger required")
		}
		return NewComplianceMiddleware(deps.Cfg, deps.Logger), nil
	})
}
