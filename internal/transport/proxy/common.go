package proxy

import (
	"context"
	"encoding/json"
	"log/slog"

	"rua/internal/jsonrpc"
	"rua/internal/middleware"
)

// applyRequest parses body를 JSON-RPC 요청으로 파싱해 middleware chain을 통과시킨다.
// 성공 시 (수정된 body, nil), 차단 시 (에러 페이로드, error) 반환.
// JSON이 아니거나 요청이 아닌 경우 body를 그대로 반환.
func applyRequest(ctx context.Context, body []byte, chain *middleware.Chain, logger *slog.Logger) ([]byte, error) {
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

// applyResponse parses body를 JSON-RPC 응답으로 파싱해 middleware chain을 통과시킨다.
// 차단되면 nil 반환. 에러 시 원본 body 반환.
func applyResponse(ctx context.Context, body []byte, chain *middleware.Chain, logger *slog.Logger) []byte {
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
