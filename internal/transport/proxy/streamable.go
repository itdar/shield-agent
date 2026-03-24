package proxy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/itdar/shield-agent/internal/middleware"
)

// StreamableProxy는 Streamable HTTP transport를 사용하는 MCP 클라이언트와
// 업스트림 MCP 서버 사이에서 미들웨어를 적용하는 HTTP 프록시다.
//
// 프로토콜:
//   - POST /mcp (또는 /) → 요청에 미들웨어 적용 후 업스트림 /mcp 포워딩, 응답 relay
type StreamableProxy struct {
	upstream string
	chain    *middleware.Chain
	logger   *slog.Logger
	client   *http.Client
}

// NewStreamableProxy creates a new Streamable HTTP proxy.
func NewStreamableProxy(upstream string, chain *middleware.Chain, logger *slog.Logger) *StreamableProxy {
	return &StreamableProxy{
		upstream: strings.TrimRight(upstream, "/"),
		chain:    chain,
		logger:   logger,
		client:   &http.Client{Timeout: 60 * time.Second},
	}
}

// Handler returns the http.Handler for this proxy.
// POST /mcp 와 POST / 둘 다 처리한다.
func (p *StreamableProxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", p.handleMCP)
	mux.HandleFunc("/mcp/", p.handleMCP)
	mux.HandleFunc("/", p.handleMCP)
	return mux
}

func (p *StreamableProxy) handleMCP(w http.ResponseWriter, r *http.Request) {
	// CORS preflight.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, DELETE")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Streamable HTTP는 GET(SSE 스트림 열기), POST(메시지 전송), DELETE(세션 종료) 지원.
	// POST가 핵심.
	if r.Method != http.MethodPost && r.Method != http.MethodGet && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// GET/DELETE는 세션 관리용 — 미들웨어 없이 그대로 프록시.
	if r.Method == http.MethodGet || r.Method == http.MethodDelete {
		p.proxyRaw(w, r)
		return
	}

	// POST: 미들웨어 적용.
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "request read error", http.StatusBadRequest)
		return
	}

	p.logger.Debug("streamable: request received",
		slog.String("path", r.URL.Path),
		slog.Int("bytes", len(body)),
	)

	// 미들웨어 체인 적용 (인증 + 로깅).
	body, chainErr := applyRequest(r.Context(), body, p.chain, p.logger)
	if chainErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write(body) //nolint:errcheck
		return
	}

	// 업스트림 URL 결정.
	// 요청 경로 기준으로 upstream URL 구성.
	reqPath := r.URL.Path
	if reqPath == "/" || reqPath == "" {
		reqPath = "/mcp"
	}
	upstreamURL := p.upstream + reqPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		p.logger.Error("streamable: upstream request creation failed", slog.String("error", err.Error()))
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	// 원본 헤더 중 필요한 것 복사.
	upReq.Header.Set("Content-Type", "application/json")
	if accept := r.Header.Get("Accept"); accept != "" {
		upReq.Header.Set("Accept", accept)
	}
	if sessionID := r.Header.Get("Mcp-Session-Id"); sessionID != "" {
		upReq.Header.Set("Mcp-Session-Id", sessionID)
	}

	upResp, err := p.client.Do(upReq)
	if err != nil {
		p.logger.Error("streamable: upstream request failed",
			slog.String("url", upstreamURL),
			slog.String("error", err.Error()),
		)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer upResp.Body.Close()

	// 업스트림 응답 헤더 relay.
	for k, vs := range upResp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(upResp.StatusCode)

	// 응답이 SSE 스트림인 경우 청크 단위로 relay + 미들웨어 적용.
	ct := upResp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		p.relaySSEResponse(w, upResp.Body)
		return
	}

	// 단순 JSON 응답: 전체 읽어서 ProcessResponse 적용 후 반환.
	respBody, err := io.ReadAll(upResp.Body)
	if err != nil {
		return
	}

	out := applyResponse(r.Context(), respBody, p.chain, p.logger)
	if out == nil {
		out = respBody // 차단된 경우 원본 유지 (streamable에서는 드롭보다 통과가 안전)
	}
	w.Write(out) //nolint:errcheck
}

// proxyRaw는 GET/DELETE 같은 메서드를 미들웨어 없이 그대로 업스트림에 포워딩한다.
func (p *StreamableProxy) proxyRaw(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Path
	if reqPath == "/" || reqPath == "" {
		reqPath = "/mcp"
	}
	upstreamURL := p.upstream + reqPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	// 헤더 복사.
	for k, vs := range r.Header {
		for _, v := range vs {
			upReq.Header.Add(k, v)
		}
	}

	upResp, err := p.client.Do(upReq)
	if err != nil {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer upResp.Body.Close()

	for k, vs := range upResp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(upResp.StatusCode)

	ct := upResp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		p.relaySSEResponse(w, upResp.Body)
		return
	}
	io.Copy(w, upResp.Body) //nolint:errcheck
}

// relaySSEResponse는 업스트림 SSE 스트림을 클라이언트에 실시간으로 relay한다.
// 각 이벤트의 data 필드에 미들웨어(ProcessResponse)를 적용한다.
func (p *StreamableProxy) relaySSEResponse(w http.ResponseWriter, body io.Reader) {
	flusher, canFlush := w.(http.Flusher)
	scanner := bufio.NewScanner(body)
	var lines []string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			ev := parseSSEEvent(lines)
			lines = lines[:0]

			var sb strings.Builder
			if ev.typ != "" {
				sb.WriteString("event: ")
				sb.WriteString(ev.typ)
				sb.WriteString("\n")
			}
			if ev.data != "" {
				// ProcessResponse 적용.
				data := applyResponse(context.Background(), []byte(ev.data), p.chain, p.logger)
				if data == nil {
					data = []byte(ev.data) // 차단 시 원본 유지
				}
				sb.WriteString("data: ")
				sb.Write(data)
				sb.WriteString("\n")
			}
			sb.WriteString("\n")

			w.Write([]byte(sb.String())) //nolint:errcheck
			if canFlush {
				flusher.Flush()
			}
		} else {
			// 이벤트 라인이 아닌 것 (id:, retry: 등)은 그대로 통과.
			if !strings.HasPrefix(line, "event: ") && !strings.HasPrefix(line, "data: ") {
				w.Write([]byte(line + "\n")) //nolint:errcheck
			} else {
				lines = append(lines, line)
			}
		}
	}
}
