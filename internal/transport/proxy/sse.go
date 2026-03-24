// Package proxy provides HTTP proxy handlers for MCP SSE and Streamable HTTP transports.
package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/itdar/shield-agent/internal/middleware"
)

// SSEProxy는 SSE transport를 사용하는 MCP 클라이언트(Claude Desktop 등)와
// 업스트림 MCP 서버 사이에서 미들웨어를 적용하는 HTTP 프록시다.
//
// 프로토콜:
//   - GET  /sse          → 업스트림 SSE 연결 후 이벤트 relay
//   - POST /messages     → 요청에 미들웨어 적용 후 업스트림 /messages 포워딩
type SSEProxy struct {
	upstream string
	chain    *middleware.Chain
	logger   *slog.Logger
	sessions *sessionStore
}

// NewSSEProxy creates a new SSE proxy.
func NewSSEProxy(upstream string, chain *middleware.Chain, logger *slog.Logger) *SSEProxy {
	return &SSEProxy{
		upstream: strings.TrimRight(upstream, "/"),
		chain:    chain,
		logger:   logger,
		sessions: newSessionStore(),
	}
}

// Handler returns the http.Handler for this proxy.
func (p *SSEProxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", p.handleSSE)
	mux.HandleFunc("/messages", p.handleMessages)
	mux.HandleFunc("/messages/", p.handleMessages)
	return mux
}

func (p *SSEProxy) handleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported by this server", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()

	// 업스트림 SSE 연결.
	upSSEURL := p.upstream + "/sse"
	upReq, err := http.NewRequestWithContext(ctx, http.MethodGet, upSSEURL, nil)
	if err != nil {
		p.logger.Error("sse: upstream request creation failed", slog.String("error", err.Error()))
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	upReq.Header.Set("Accept", "text/event-stream")
	upReq.Header.Set("Cache-Control", "no-cache")

	// SSE는 timeout 없이 long-lived connection.
	upClient := &http.Client{Timeout: 0}
	upResp, err := upClient.Do(upReq)
	if err != nil {
		p.logger.Error("sse: upstream connection failed",
			slog.String("url", upSSEURL),
			slog.String("error", err.Error()),
		)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	defer upResp.Body.Close()

	// 로컬 세션 생성.
	localID := uuid.New().String()
	sess := &session{
		events: make(chan []byte, 512),
	}
	p.sessions.add(localID, sess)
	defer p.sessions.remove(localID)

	p.logger.Info("sse: session started", slog.String("session_id", localID))

	// done 채널로 goroutine 종료 신호 전달.
	done := make(chan struct{})
	defer close(done)

	// 업스트림 SSE 읽기 goroutine.
	go func() {
		defer close(sess.events)

		scanner := bufio.NewScanner(upResp.Body)
		var lines []string

		send := func(event []byte) bool {
			select {
			case sess.events <- event:
				return true
			case <-done:
				return false
			}
		}

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				// SSE 이벤트 블록 완성.
				ev := parseSSEEvent(lines)
				lines = lines[:0]

				switch ev.typ {
				case "endpoint":
					// 업스트림 메시지 URL 저장 (절대 URL로 변환).
					upMsgURL := ev.data
					if strings.HasPrefix(upMsgURL, "/") {
						upMsgURL = p.upstream + upMsgURL
					}
					sess.setUpstreamMsgURL(upMsgURL)

					// 클라이언트에게는 우리 로컬 엔드포인트를 알려준다.
					localPath := fmt.Sprintf("/messages?sessionId=%s", localID)
					event := fmt.Sprintf("event: endpoint\ndata: %s\n\n", localPath)
					p.logger.Debug("sse: endpoint established",
						slog.String("upstream_msg_url", upMsgURL),
						slog.String("local_path", localPath),
					)
					if !send([]byte(event)) {
						return
					}

				default:
					if ev.data == "" {
						continue
					}
					// 응답 데이터에 미들웨어 적용 (LogMiddleware가 레이턴시 기록).
					data := applyResponse(ctx, []byte(ev.data), p.chain, p.logger)
					if data == nil {
						continue // 미들웨어가 차단
					}

					var sb strings.Builder
					if ev.typ != "" {
						sb.WriteString("event: ")
						sb.WriteString(ev.typ)
						sb.WriteString("\n")
					}
					sb.WriteString("data: ")
					sb.Write(data)
					sb.WriteString("\n\n")
					if !send([]byte(sb.String())) {
						return
					}
				}
			} else {
				lines = append(lines, line)
			}
		}

		if err := scanner.Err(); err != nil {
			p.logger.Debug("sse: upstream scanner stopped", slog.String("error", err.Error()))
		}
	}()

	// 클라이언트에 SSE 헤더 설정.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// 세션 채널에서 이벤트를 꺼내 클라이언트로 relay.
	for {
		select {
		case event, ok := <-sess.events:
			if !ok {
				p.logger.Info("sse: upstream closed", slog.String("session_id", localID))
				return
			}
			if _, err := w.Write(event); err != nil {
				p.logger.Debug("sse: client write error", slog.String("error", err.Error()))
				return
			}
			flusher.Flush()
		case <-ctx.Done():
			p.logger.Info("sse: client disconnected", slog.String("session_id", localID))
			return
		}
	}
}

func (p *SSEProxy) handleMessages(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "missing sessionId query param", http.StatusBadRequest)
		return
	}

	sess, ok := p.sessions.get(sessionID)
	if !ok {
		p.logger.Warn("sse: unknown session", slog.String("session_id", sessionID))
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}

	upMsgURL := sess.getUpstreamMsgURL()
	if upMsgURL == "" {
		http.Error(w, "session not ready yet (endpoint not received from upstream)", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "request read error", http.StatusBadRequest)
		return
	}

	p.logger.Debug("sse: message received",
		slog.String("session_id", sessionID),
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

	// 업스트림으로 포워딩.
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upMsgURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "upstream request error", http.StatusBadGateway)
		return
	}
	upReq.Header.Set("Content-Type", "application/json")

	upClient := &http.Client{Timeout: 30 * time.Second}
	upResp, err := upClient.Do(upReq)
	if err != nil {
		p.logger.Error("sse: upstream message forward failed", slog.String("error", err.Error()))
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer upResp.Body.Close()

	// 업스트림 응답 코드 그대로 반환 (보통 202 Accepted).
	w.WriteHeader(upResp.StatusCode)
}

// sseEvent holds a parsed SSE event block.
type sseEvent struct {
	typ  string
	data string
}

func parseSSEEvent(lines []string) sseEvent {
	var ev sseEvent
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "event: "):
			ev.typ = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
		}
	}
	return ev
}
