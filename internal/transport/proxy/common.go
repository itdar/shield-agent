package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"

	"github.com/itdar/shield-agent/internal/jsonrpc"
	"github.com/itdar/shield-agent/internal/middleware"
)

// buildUpstreamURL constructs the upstream URL by respecting any path already
// present in the configured upstream base URL. If the upstream has no path
// (e.g. "http://localhost:8000"), the request path is appended (defaulting to
// "/mcp"). If the upstream already includes a path (e.g.
// "https://host/mcp"), it is used as-is to avoid double-path issues like
// "/mcp/mcp".
func buildUpstreamURL(upstream, reqPath, rawQuery string) string {
	u, err := url.Parse(upstream)
	if err != nil || (u.Path == "" || u.Path == "/") {
		// Upstream has no meaningful path — append request path.
		if reqPath == "/" || reqPath == "" {
			reqPath = "/mcp"
		}
		result := strings.TrimRight(upstream, "/") + reqPath
		if rawQuery != "" {
			result += "?" + rawQuery
		}
		return result
	}
	// Upstream already has a path — use it as-is.
	result := upstream
	if rawQuery != "" {
		result += "?" + rawQuery
	}
	return result
}

// applyRequestWithIP is like applyRequest but injects the client IP into the
// middleware context so that auth and log middlewares can record it.
func applyRequestWithIP(ctx context.Context, body []byte, chain *middleware.SwappableChain, logger *slog.Logger, remoteAddr string) (context.Context, []byte, error) {
	ctx, ar := middleware.WithAuthResult(ctx)
	// Extract IP from RemoteAddr (host:port).
	if idx := strings.LastIndex(remoteAddr, ":"); idx != -1 {
		ar.IPAddress = remoteAddr[:idx]
	} else {
		ar.IPAddress = remoteAddr
	}
	out, err := applyRequest(ctx, body, chain, logger)
	return ctx, out, err
}

// applyRequest parses body as a JSON-RPC request and passes it through the middleware chain.
// Returns (modified body, nil) on success, or (error payload, error) if blocked.
// Returns body unchanged if not valid JSON or not a request.
func applyRequest(ctx context.Context, body []byte, chain *middleware.SwappableChain, logger *slog.Logger) ([]byte, error) {
	if chain == nil {
		return body, nil
	}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return body, nil
	}
	if _, hasMethod := probe["method"]; !hasMethod {
		return body, nil
	}

	var req jsonrpc.Request
	if err := json.Unmarshal(body, &req); err != nil {
		return body, nil
	}

	modified, errPayload, chainErr := chain.ProcessRequest(ctx, &req)
	if chainErr != nil {
		logger.Warn("proxy: request blocked",
			slog.String("method", req.Method),
			slog.String("error", chainErr.Error()),
		)
		return errPayload, chainErr
	}

	out, err := json.Marshal(modified)
	if err != nil {
		return body, nil
	}
	return out, nil
}

// applyResponse parses body as a JSON-RPC response and passes it through the middleware chain.
// Returns nil if blocked. Returns original body on error.
func applyResponse(ctx context.Context, body []byte, chain *middleware.SwappableChain, logger *slog.Logger) []byte {
	if chain == nil {
		return body
	}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return body
	}
	_, hasResult := probe["result"]
	_, hasError := probe["error"]
	if !hasResult && !hasError {
		return body
	}

	var resp jsonrpc.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return body
	}

	modified, chainErr := chain.ProcessResponse(ctx, &resp)
	if chainErr != nil {
		logger.Warn("proxy: response blocked", slog.String("error", chainErr.Error()))
		return nil
	}

	out, err := json.Marshal(modified)
	if err != nil {
		return body
	}
	return out
}
