# AGENTS.md — rua (shield-agent) 백엔드 엔지니어 (Go)

## 역할
MCP/A2A 보안 미들웨어 프록시(`shield-agent`)를 개발한다. CLI 래퍼(stdio) 모드와 HTTP 프록시 모드를 제공하며, 인증/로깅/텔레메트리/토큰 관리/가드 미들웨어 체인을 구성한다.

## 명령어
- 빌드: `go build ./cmd/shield-agent`
- 테스트: `go test ./...`
- 실행(stdio): `go run ./cmd/shield-agent <command>`
- 실행(proxy): `go run ./cmd/shield-agent proxy --listen :8888 --upstream <url>`
- 린트: `go vet ./...`

## 구조
- `cmd/shield-agent/` — CLI 엔트리포인트 (stdio, proxy, token, logs, egress, ca 서브커맨드)
- `internal/middleware/` — JSON-RPC 미들웨어 체인 (auth, log, token, guard) + A2A/HTTP API 변형, `correlation.go` (128-bit request id)
- `internal/transport/proxy/` — SSE, Streamable HTTP 프록시 핸들러 + 멀티 업스트림 라우터
- `internal/egress/` — Phase 5 forward-proxy egress. `middleware.go` (EgressMiddleware/Chain/SwappableChain), `registry.go`, `proxy.go` (CONNECT + MITM), `ca.go` + `mitm.go` (TLS MITM), `provider_detect.go`
- `internal/compliance/` — egress 감사 파이프라인. `log_writer.go` (drop-free), `hashchain.go`, `policy.go`, `pii_scrub.go`, `content_tag.go`, `middleware.go`, `digest.go`, `export.go`, `egress_log_mw.go`
- `internal/webui/` — 관리 Web UI API (`/api/dashboard`, `/api/logs`, `/api/tokens`, `/api/keys`, `/api/middlewares`, `/api/upstreams`)
- `internal/storage/` — SQLite (action_logs, agent_keys, upstreams, config, egress_logs, egress_log_anchors)
- `internal/token/` — API 토큰 CRUD + 사용량 추적
- `internal/auth/` — Ed25519 서명 검증, KeyStore (파일+DB+캐시 복합)
- `internal/telemetry/` — 차등 프라이버시 적용 텔레메트리 수집기

## 컨벤션
- 커밋 메시지 **영어 전용** (도메인 규칙 오버라이드)
- Go 모듈: `github.com/itdar/shield-agent`

## 권한
- `rua/` 하위 모든 파일 수정 가능
- `ripe/` 수정 불가

## 컨텍스트 유지
- 미들웨어 추가/삭제 시 상위 `.ai-agents/context/api-spec.json` 갱신 요청
- 텔레메트리 전송 포맷 변경 시 ripe 팀과 동기화 필수
- **Phase 5 egress 설계 문서**: `.omc/plans/egress-compliance.md` (RALPLAN-DR consensus plan, Architect/Critic APPROVE at iteration 2). 스키마·정책·체인 알고리즘 수정 전 반드시 참조
- 새 `egress_logs` 컬럼을 추가하면 `internal/compliance/hashchain.go`의 `canonicalRowHash` 및 `VerifyResult` 검증 순회도 동일하게 확장해야 체인이 깨지지 않음
