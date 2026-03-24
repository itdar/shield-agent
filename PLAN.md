# PLAN.md — 실행 계획

> TODO.md Phase 1 (Core MVP) 구현을 위한 체크포인트별 실행 계획.
> 각 체크포인트는 **빌드 통과 + 테스트 통과** 상태로 커밋한다.

---

## 현재 코드 상태 진단

```
유지할 것 (핵심 로직 건전)         정리/리팩토링 필요              삭제 대상
─────────────────────────      ─────────────────────────     ──────────────────
internal/auth/auth.go          go.mod (rua → github path)    internal/telemetry/  (Phase 4)
internal/storage/db.go         env vars (MCP_SHIELD_ →       internal/middleware/a2a/  (통합)
internal/jsonrpc/              SHIELD_AGENT_*)                internal/middleware/httpapi/  (통합)
internal/process/              Prometheus (mcp_shield_ →      LOCAL_TEST*.md  (4개)
internal/config/               shield_agent_*)                REAL_TEST.md
internal/monitor/              한글 주석 → 영어               mcp-shield (바이너리)
internal/transport/proxy/      middleware 하드코딩 → 동적     .idea/ (gitignore)
cmd/shield-agent/              CORS * 하드코딩 → 설정
```

---

## CP-0: 프로젝트 기반 정리 (클린업)

> 불필요한 파일 삭제, go.mod 경로, .gitignore 정리

- [ ] 삭제: `LOCAL_TEST.md`, `LOCAL_TEST_A2A.md`, `LOCAL_TEST_HTTPAPI.md`, `LOCAL_TEST_JSONRPC.md`, `REAL_TEST.md`
- [ ] 삭제: `mcp-shield` (빌드 바이너리)
- [ ] 삭제: `.idea/` 디렉토리
- [ ] `.gitignore`에 추가: `.idea/`, `shield-agent` (바이너리), `*.db`
- [ ] `go.mod`: `module rua` → `module github.com/itdar/shield-agent`
- [ ] 전체 import path 일괄 변경: `rua/internal/...` → `github.com/itdar/shield-agent/internal/...`
- [ ] `go build ./cmd/shield-agent` 성공 확인
- [ ] `go test ./...` 통과 확인
- [ ] **커밋**: `chore: update module path and clean up project files`

---

## CP-1: 네이밍 통일 (env vars, metrics)

> `MCP_SHIELD_*` → `SHIELD_AGENT_*`, Prometheus 메트릭명 통일

- [ ] `internal/config/config.go`: 환경변수 prefix `MCP_SHIELD_` → `SHIELD_AGENT_`
- [ ] `shield-agent.example.yaml`: 주석 내 환경변수명 업데이트
- [ ] `internal/monitor/server.go`: Prometheus 메트릭명 변경
  - `mcp_shield_messages_total` → `shield_agent_messages_total`
  - `mcp_shield_auth_total` → `shield_agent_auth_total`
  - `mcp_shield_message_latency_seconds` → `shield_agent_message_latency_seconds`
  - `mcp_shield_child_process_up` → `shield_agent_child_process_up`
- [ ] 테스트 파일 내 참조 업데이트
- [ ] `go test ./...` 통과 확인
- [ ] **커밋**: `refactor: rename env vars and metrics from mcp_shield to shield_agent`

---

## CP-2: 주석 영어화

> 모든 한글 주석을 영어로 변환

- [ ] `cmd/shield-agent/proxy.go`: 한글 주석 → 영어
- [ ] `internal/transport/proxy/sse.go`: 한글 주석 → 영어
- [ ] `internal/transport/proxy/streamable.go`: 한글 주석 → 영어
- [ ] `internal/transport/proxy/session.go`: 한글 주석 → 영어 (있을 경우)
- [ ] 기타 한글 주석 남은 파일 전수 검사 (`grep -r "[가-힣]" --include="*.go"`)
- [ ] **커밋**: `chore: translate all comments to English`

---

## CP-3: Middleware 인터페이스 리팩토링

> `Name()` 메서드 추가, 동적 체인 구성 기반 마련

### 3-1. 인터페이스 변경

- [ ] `internal/middleware/middleware.go`: `Middleware` 인터페이스에 `Name() string` 추가
- [ ] `PassthroughMiddleware`에 `Name()` 구현 (빈 문자열 반환)
- [ ] `AuthMiddleware.Name()` → `"auth"` 반환
- [ ] `LogMiddleware.Name()` → `"log"` 반환

### 3-2. 동적 Chain 구성

- [ ] `internal/config/config.go`에 `MiddlewareConfig` 구조체 추가
  ```go
  type MiddlewareEntry struct {
      Name    string         `yaml:"name"`
      Enabled bool           `yaml:"enabled"`
      Config  map[string]any `yaml:"config"`
  }
  ```
- [ ] `Config`에 `Middlewares []MiddlewareEntry` 필드 추가
- [ ] `internal/middleware/registry.go` 생성: 이름 → 생성함수 매핑
  ```go
  type Factory func(cfg map[string]any, deps Dependencies) (Middleware, error)
  var registry = map[string]Factory{ "auth": newAuth, "log": newLog, ... }
  func BuildChain(entries []MiddlewareEntry, deps Dependencies) (*Chain, error)
  ```
- [ ] `cmd/shield-agent/stdio.go`, `proxy.go`: 하드코딩된 체인 → `BuildChain()` 호출로 교체
- [ ] CLI 플래그: `--disable-middleware`, `--enable-middleware` 추가
- [ ] `shield-agent.example.yaml` 업데이트 (middlewares 섹션 추가)
- [ ] 기존 테스트 수정 + middleware chain 동적 구성 테스트 추가
- [ ] `go test ./...` 통과 확인
- [ ] **커밋**: `feat(middleware): add dynamic middleware chain with YAML configuration`

---

## CP-4: Guard Middleware (신규)

> Rate limiting, request size limit, IP blocklist

- [ ] `internal/middleware/guard.go` 생성
  ```go
  type GuardMiddleware struct {
      rateLimiter *RateLimiter
      maxBodySize int64
      ipBlocklist []net.IPNet
      ipAllowlist []net.IPNet
  }
  ```
- [ ] Rate Limiter 구현
  - sliding window counter (IP별, Agent별)
  - 인메모리 `sync.Map` + 주기적 정리 goroutine
  - 초과 시: JSON-RPC → error response, HTTP → 429
- [ ] Request size limit: body 크기 검사 (기본 10MB)
- [ ] IP blocklist/allowlist: CIDR 매칭
- [ ] Brute force 방어: 연속 실패 N회 시 자동 임시 차단
- [ ] `Name()` → `"guard"` 반환
- [ ] registry에 등록
- [ ] 테스트: rate limit 초과, IP 차단, size 초과, brute force
- [ ] `go test ./...` 통과 확인
- [ ] **커밋**: `feat(middleware): add guard middleware with rate limiting, IP filter, and size limit`

---

## CP-5: A2A / HTTP API middleware 통합

> 현재 별도 패키지 (a2a/, httpapi/) → MCP middleware와 통일된 인터페이스로 통합

- [ ] `internal/middleware/a2a/`, `internal/middleware/httpapi/` 코드 분석
  - 핵심 차이: HTTP handler wrapping vs JSON-RPC pipeline
  - 공통: Ed25519 검증, 로깅, 텔레메트리
- [ ] HTTP 기반 middleware 전용 인터페이스 유지 (transport 레벨에서 사용)
  ```go
  // internal/middleware/http.go
  type HTTPMiddleware interface {
      Name() string
      WrapHandler(next http.Handler) http.Handler
  }
  ```
- [ ] Auth/Log 로직 공유를 위해 내부 함수 추출 → 중복 제거
- [ ] A2A, HTTP API의 auth/log를 공통 코어 함수 기반으로 재작성
- [ ] 기존 테스트 마이그레이션 + 통합 테스트
- [ ] `go test ./...` 통과 확인
- [ ] **커밋**: `refactor(middleware): unify A2A and HTTP API middleware with shared auth/log core`

---

## CP-6: TLS 지원 + CORS 설정

- [ ] `internal/config/config.go`에 TLS/CORS 설정 추가
  ```go
  type ServerConfig struct {
      MonitorAddr    string   `yaml:"monitor_addr"`
      TLSCert        string   `yaml:"tls_cert"`
      TLSKey         string   `yaml:"tls_key"`
      CORSAllowedOrigins []string `yaml:"cors_allowed_origins"` // default: ["*"]
  }
  ```
- [ ] `cmd/shield-agent/proxy.go`: `--tls-cert`, `--tls-key` 플래그
- [ ] proxy 서버: TLS 설정 시 `ListenAndServeTLS` 사용
- [ ] CORS 헤더: `Access-Control-Allow-Origin: *` 하드코딩 제거 → 설정에서 읽기
- [ ] `shield-agent.example.yaml` 업데이트
- [ ] `go test ./...` 통과 확인
- [ ] **커밋**: `feat(server): add TLS support and configurable CORS`

---

## CP-7: SIGHUP 런타임 리로드

- [ ] SIGHUP 시그널 핸들러 추가 (stdio, proxy 모드 모두)
- [ ] 리로드 대상: YAML 설정 전체 (middleware chain, keys.yaml, IP 목록 등)
- [ ] 리로드 시 기존 연결 유지, 새 요청부터 적용
- [ ] 로그 출력: `"configuration reloaded"`
- [ ] 테스트: SIGHUP 전후 설정 변경 확인
- [ ] `go test ./...` 통과 확인
- [ ] **커밋**: `feat(config): add SIGHUP runtime configuration reload`

---

## CP-8: Prometheus 메트릭 보강

- [ ] Guard middleware 메트릭 추가
  - `shield_agent_rate_limit_rejected_total` (Counter, label: agent_hash)
  - `shield_agent_blocked_ip_total` (Counter)
- [ ] Log middleware에서 메트릭 연동 개선
  - `shield_agent_messages_total` 카운터 실제 증가 (현재 미연동 부분 확인)
  - `shield_agent_message_latency_seconds` 히스토그램 기록
- [ ] proxy 모드 healthz: upstream health check 추가
- [ ] `go test ./...` 통과 확인
- [ ] **커밋**: `feat(monitor): add guard metrics and improve health checks`

---

## CP-9: README.md 업데이트

> 변경된 설정, 메트릭명, 새 기능 반영

- [ ] README.md 전면 업데이트
  - env vars: `SHIELD_AGENT_*`
  - Prometheus 메트릭명: `shield_agent_*`
  - Guard middleware 섹션 추가
  - 동적 middleware chain YAML 예시
  - TLS/CORS 설정 섹션
- [ ] `shield-agent.example.yaml` 최종 정리
- [ ] **커밋**: `docs: update README for new features and naming`

---

## CP-10: 전체 검증

> TODO.md Phase 1 완료 확인

### 빌드 & 테스트

- [ ] `go vet ./...` — 0 errors
- [ ] `go build ./cmd/shield-agent` — 성공
- [ ] `go test ./... -v -count=1` — all PASS
- [ ] `go test -race ./...` — race condition 없음

### TODO.md 체크리스트 대조

- [ ] 1.1 프로젝트 기반: go.mod 경로, 디렉토리 구조, 영어 주석, CI 설정파일
- [ ] 1.2 Transport: stdio 모드, proxy 모드 (SSE + Streamable HTTP), TLS, CORS
- [ ] 1.3 Middleware Chain: 동적 YAML 구성, CLI 오버라이드, SIGHUP 리로드
- [ ] 1.4 Auth Middleware: Ed25519, DID, open/closed, KeyStore
- [ ] 1.5 Guard Middleware: rate limit, size limit, IP blocklist, brute force 방어
- [ ] 1.6 Log Middleware: 비동기 SQLite 저장, IP 추가
- [ ] 1.7 Storage: WAL, 인덱스, purge
- [ ] 1.8 설정 시스템: 우선순위 (CLI > env > YAML > default)
- [ ] 1.9 모니터링: /healthz, /metrics, 신규 메트릭
- [ ] 1.10 Log 조회 CLI: logs 서브커맨드
- [ ] 1.11 테스트: 각 모듈 단위 테스트 + e2e

### 수동 테스트 (가능 시)

- [ ] stdio 모드: `shield-agent echo "hello"` 실행 → 정상 통과 확인
- [ ] proxy 모드: `shield-agent proxy --listen :8888 --upstream http://httpbin.org` → 요청 포워딩 확인
- [ ] logs CLI: `shield-agent logs --last 10` → 테이블 출력 확인
- [ ] rate limit: 초과 요청 시 429 / JSON-RPC error 반환 확인
- [ ] SIGHUP: 설정 변경 후 `kill -HUP <pid>` → 반영 확인

### 최종

- [ ] TODO.md 체크박스 업데이트 (완료 항목 체크)
- [ ] **커밋**: `chore: mark Phase 1 TODO items as complete`

---

## CP-11: 실제 사용자 테스트 방법
- [ ] 사용자가 실제로 사용하면, 빌드된 것 받았을 경우 각각의 사용 케이스별로 test 방법 문서 자세히 작성
- [ ] (가능하다면) 해당 문서 방법대로 실제 테스트.
- [ ] 최종 커밋 푸시.

---

## 체크포인트 순서 요약

```
CP-0  프로젝트 클린업          (삭제 + go.mod + import path)
CP-1  네이밍 통일              (env vars + Prometheus metrics)
CP-2  주석 영어화              (한글 → 영어)
CP-3  Middleware 리팩토링      (Name() + 동적 chain + registry)
CP-4  Guard Middleware         (rate limit + IP + size + brute force)
CP-5  A2A/HTTPAPI 통합        (중복 제거 + 공통 코어)
CP-6  TLS + CORS              (설정 기반)
CP-7  SIGHUP 리로드            (런타임 설정 변경)
CP-8  메트릭 보강              (guard 메트릭 + healthz 개선)
CP-9  README 업데이트          (문서 동기화)
CP-10 전체 검증                (빌드 + 테스트 + TODO 대조 + 수동 테스트)
CP-11 실제 사용자 테스트 방법     (test 방법 문서 작성)
```

**예상 커밋 수**: 10~12개 (체크포인트당 1커밋, 일부 분할 가능)
