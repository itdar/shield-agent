package proxy

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/itdar/shield-agent/internal/jsonrpc"
	"github.com/itdar/shield-agent/internal/middleware"
)

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
