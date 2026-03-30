# shield-agent — 프로젝트 설계 & 구현 로드맵

> AI Agent 시대의 보안 미들웨어. Agent ↔ Server 사이에서 인증, 방어, 로깅, 모니터링을 담당한다.

---

## 비전

```
┌──────────┐     ┌──────────────┐     ┌──────────────────┐
│ AI Agent │────▶│ shield-agent │────▶│ Target Server    │
│          │◀────│ (middleware) │◀────│ (MCP/A2A/API)    │
└──────────┘     └──────────────┘     └──────────────────┘
                       │
                 ┌─────┴─────┐
                 │ SQLite DB │  logging, tokens, reputation
                 └───────────┘
```

**핵심 가치**: 설치 30초, 설정 1분, Agent 트래픽 즉시 보호.

---

## 보호 대상 통신 케이스

```
Case 1:  Agent ──▶ MCP Server      (stdio wrapping / HTTP proxy)
Case 2:  Agent ──▶ Agent           (A2A, Google A2A protocol)
Case 3:  Agent ──▶ API Server      (일반 HTTP REST/GraphQL)
Case 4:  Agent ──▶ MCP ──▶ MCP     (MCP 체이닝 — 서버가 다른 서버 호출)
```

각 케이스에 동일한 middleware chain이 적용됨.

---

## 아키텍처 개요

```
                        shield-agent
┌─────────────────────────────────────────────────┐
│                                                 │
│  ┌─────────┐   ┌─────────────────────────────┐  │
│  │  CLI    │──▶│     Transport Layer         │  │
│  │  flags  │   │  ┌───────┐ ┌─────┐ ┌─────┐  │  │
│  │  + YAML │   │  │ stdio │ │ SSE │ │ HTTP│  │  │
│  └─────────┘   │  └───┬───┘ └──┬──┘ └──┬──┘  │  │
│                │      └────────┴───────┘     │  │
│                └─────────────┬───────────────┘  │
│                              ▼                  │
│                ┌─────────────────────────────┐  │
│                │    Middleware Chain         │  │
│                │  ┌─────┐┌─────┐┌─────┐┌───┐ │  │
│                │  │Auth ││Rate ││Guard││Log│ │  │
│                │  │     ││Limit││     ││   │ │  │
│                │  └─────┘└─────┘└─────┘└───┘ │  │
│                └─────────────┬───────────────┘  │
│                              ▼                  │
│  ┌──────────┐  ┌──────────┐  ┌───────────────┐  │
│  │ SQLite   │  │ Monitor  │  │ Token Manager │  │
│  │ (logs,   │  │ (metrics,│  │ (issue, quota,│  │
│  │  tokens) │  │  health) │  │  access ctrl) │  │
│  └──────────┘  └──────────┘  └───────────────┘  │
│                                                 │
│  ┌──────────────────────────────────────────┐   │
│  │           Web UI (Monitor + Control)     │   │
│  └──────────────────────────────────────────┘   │
└─────────────────────────────────────────────────┘
```

---

## Phase 1 — Core (MVP)

> 목표: `go build` 하나로 바로 사용 가능한 최소 기능

### 1.1 프로젝트 기반

- [ ] go.mod module path → `github.com/itdar/shield-agent`
- [ ] 디렉토리 구조 정리
  ```
  cmd/shield-agent/     CLI 엔트리포인트
  internal/
    transport/          stdio, proxy (SSE, streamable-http)
    middleware/         middleware 인터페이스 + 체인
    middleware/auth/    Ed25519/DID 인증
    middleware/guard/   rate-limit, size-limit, IP block
    middleware/log/     요청/응답 로깅
    middleware/token/   토큰 인증
    auth/               KeyStore, DID 해석
    storage/            SQLite (logs, tokens, config)
    config/             YAML + env + CLI 우선순위
    monitor/            /healthz, /metrics, Web UI
    token/              토큰 발급/관리/quota
  ```
- [ ] 모든 주석 영어. 파일/기능 단위로 핵심만 짧게
- [ ] CI/CD: GitHub Actions (build, test, lint, vet)
- [ ] `.goreleaser.yml` 설정

### 1.2 Transport Layer

- [ ] **stdio 모드**: `shield-agent <command> [args...]`
  - 자식 프로세스 stdin/stdout 파이프 인터셉트
  - stderr 패스스루
  - SIGINT/SIGTERM 포워딩, exit code 전파
  - graceful shutdown (5초 → SIGKILL)
- [ ] **proxy 모드**: `shield-agent proxy --listen :8888 --upstream <url>`
  - SSE transport (`GET /sse` + `POST /messages`)
  - Streamable HTTP transport (`POST /mcp`, `GET /mcp`, `DELETE /mcp`)
  - `--transport sse|streamable-http` 플래그
- [ ] **TLS 지원**: `--tls-cert`, `--tls-key` 플래그
- [ ] **CORS 설정**: YAML에서 allowed origins 제어 (현재 `*` 하드코딩 제거)

### 1.3 Middleware Chain (동적 구성)

```yaml
# shield-agent.yaml
middlewares:
  - name: auth
    enabled: true
    config:
      mode: open          # open | closed
      key_store: keys.yaml
  - name: guard
    enabled: true
    config:
      rate_limit: 100/m   # 분당 100회
      max_body: 10MB
      ip_blocklist: []
  - name: token
    enabled: false
  - name: log
    enabled: true
```

- [ ] Middleware 인터페이스 설계
  ```go
  type Middleware interface {
      Name() string
      ProcessRequest(ctx, *Request) (*Request, error)
      ProcessResponse(ctx, *Response) (*Response, error)
  }
  ```
- [ ] Chain: YAML 순서대로 로드, `enabled: false`면 스킵
- [ ] CLI 오버라이드: `--disable-middleware auth` / `--enable-middleware token`
- [ ] 런타임 reload: SIGHUP 시 YAML 재로드 (재시작 없이 적용)

### 1.4 Auth Middleware

- [ ] Ed25519 서명 검증
  - JSON-RPC: `_mcp_agent_id` + `_mcp_signature` 추출
  - HTTP: `X-Agent-ID` + `X-Agent-Signature` 헤더
  - payload = sha256(canonical JSON)
- [ ] Agent ID 해석
  - `did:key:z...` → Base58btc + multicodec(0xed01) → Ed25519 pubkey
  - plain string → `keys.yaml` 조회
- [ ] `open` 모드 (경고만) / `closed` 모드 (거부)
- [ ] KeyStore: FileKeyStore (YAML) + CachedKeyStore (TTL 5분)
- [ ] Agent ID는 로그에 sha256 해시로만 기록

### 1.5 Guard Middleware (방어)

- [ ] **Rate Limiting**
  - IP별, Agent별 분당/시간당 요청 제한
  - sliding window 알고리즘
  - 초과 시 JSON-RPC error 또는 HTTP 429 반환
- [ ] **Request Size Limit**
  - body 크기 제한 (기본 10MB, YAML로 설정 가능)
- [ ] **IP Blocklist / Allowlist**
  - YAML에서 IP/CIDR 지정
  - SIGHUP으로 동적 리로드
- [ ] **기본 공격 방어**
  - 반복 실패 요청 자동 차단 (brute force)
  - 비정상 페이로드 감지 (malformed JSON-RPC)

### 1.6 Log Middleware

- [ ] 요청/응답 쌍 SQLite 비동기 저장
  - 비동기 write channel (버퍼 512)
  - 채널 풀이면 drop + 경고 로그
- [ ] 스키마: timestamp, agent_hash, method, direction, success, latency_ms, payload_size, auth_status, error_code, ip_address
- [ ] 인덱스: timestamp, (agent_hash, timestamp), method
- [ ] 자동 purge: retention_days(기본 30) 경과 항목 삭제

### 1.7 Storage (SQLite)

- [ ] WAL 모드, busy timeout 5초
- [ ] 테이블: `action_logs`, `tokens`, `token_usage`
- [ ] 마이그레이션 시스템 (버전 관리)

### 1.8 설정 시스템

우선순위: **CLI 플래그 > 환경변수 (`SHIELD_AGENT_*`) > YAML > 기본값**

| 항목 | 기본값 | 환경변수 |
|------|--------|---------|
| `server.monitor_addr` | `127.0.0.1:9090` | `SHIELD_AGENT_MONITOR_ADDR` |
| `security.mode` | `open` | `SHIELD_AGENT_SECURITY_MODE` |
| `security.key_store_path` | `keys.yaml` | `SHIELD_AGENT_KEY_STORE_PATH` |
| `logging.level` | `info` | `SHIELD_AGENT_LOG_LEVEL` |
| `logging.format` | `json` | `SHIELD_AGENT_LOG_FORMAT` |
| `storage.db_path` | `shield-agent.db` | `SHIELD_AGENT_DB_PATH` |
| `storage.retention_days` | `30` | `SHIELD_AGENT_RETENTION_DAYS` |

### 1.9 모니터링 서버

기본: `127.0.0.1:9090`

| 엔드포인트 | 설명 |
|-----------|------|
| `/` | JSON 인덱스 |
| `/healthz` | 헬스체크 (stdio: 자식 PID 확인, proxy: upstream 확인) |
| `/metrics` | Prometheus 메트릭 |

Prometheus 메트릭:

| 메트릭명 | 타입 | 레이블 |
|---------|------|--------|
| `shield_agent_messages_total` | Counter | direction, method |
| `shield_agent_auth_total` | Counter | status |
| `shield_agent_message_latency_seconds` | Histogram | method |
| `shield_agent_child_process_up` | Gauge | — |
| `shield_agent_rate_limit_rejected_total` | Counter | agent_hash |

### 1.10 Log 조회 CLI

```
shield-agent logs --last 50 --agent <id> --since 1h --method <name> --format json|table
```

### 1.11 테스트

- [ ] auth: 서명 검증, DID 해석, open/closed 모드
- [ ] guard: rate limit 초과, IP 차단, size limit
- [ ] middleware chain: 순서, 에러 전파, 비활성화
- [ ] storage: CRUD, purge, 동시성
- [ ] transport: stdio 파이프라인, proxy 포워딩
- [ ] config: 우선순위, 검증
- [ ] e2e: 실제 MCP 서버와 통합 테스트

---

## Phase 2 — 배포 & 설치 편의성

> 목표: 누구나 30초 안에 설치해서 쓸 수 있게

### 2.1 배포

- [ ] **Dockerfile** (multi-stage, scratch 기반 ~10MB)
  ```dockerfile
  FROM golang:1.22-alpine AS build
  RUN go build -o /shield-agent ./cmd/shield-agent

  FROM scratch
  COPY --from=build /shield-agent /shield-agent
  ENTRYPOINT ["/shield-agent"]
  ```
- [ ] **docker-compose.yml** 예제
  ```yaml
  services:
    shield:
      image: ghcr.io/itdar/shield-agent:latest
      command: proxy --listen :8888 --upstream http://mcp-server:8000
      ports: ["8888:8888", "9090:9090"]
    mcp-server:
      image: your-mcp-server
  ```
- [ ] **GoReleaser**: Linux/macOS/Windows 바이너리 + Docker image 자동 빌드
- [ ] **Homebrew tap**: `brew install itdar/tap/shield-agent`
- [ ] **curl 설치 스크립트**: `curl -sSL https://get.shield-agent.dev | sh`
- [ ] **GitHub Releases**: 태그 push → CI 자동 배포

### 2.2 문서

- [ ] README.md (영문, 오픈소스 메인)
- [ ] docs/ko/ (한글 문서, 시각화 중심)
  - 아키텍처 다이어그램 (mermaid)
  - 미들웨어 흐름도
  - 설치 & 사용법
- [ ] CONTRIBUTING.md
- [ ] CODE_OF_CONDUCT.md
- [ ] shield-agent.example.yaml (주석 달린 전체 설정 예시)

---

## Phase 3 — 토큰 관리 & Web UI

> 목표: 토큰 기반 접근 제어 + 간단한 관리 화면

### 3.1 토큰 시스템

```
┌─────────────┐    발급     ┌──────────────┐
│ Admin (CLI  │───────────▶│ Token Store  │
│  or Web UI) │            │ (SQLite)     │
└─────────────┘            └──────┬───────┘
                                  │ 검증
┌──────────┐   토큰 포함      ┌─────▼────────┐
│ AI Agent │──────────────▶│ Token MW     │
└──────────┘               └──────────────┘
```

- [ ] **토큰 발급 CLI**: `shield-agent token create --name "agent-1" --quota 1000/h`
- [ ] **토큰 스키마**
  ```sql
  tokens: id, name, token_hash, created_at, expires_at, active,
          quota_hourly, quota_monthly, allowed_methods, ip_allowlist
  token_usage: token_id, timestamp, method, success, latency_ms
  ```
- [ ] **Token Middleware**: `Authorization: Bearer <token>` 또는 `X-Shield-Token` 헤더
- [ ] **Quota 관리**: 시간당/월간 사용량 추적, 초과 시 429
- [ ] **토큰 CLI 관리**
  ```
  shield-agent token list
  shield-agent token revoke <id>
  shield-agent token stats <id> --since 24h
  ```

### 3.2 Web UI

기술 선택: **Embedded SPA** (Go embed + 경량 프레임워크)

- [ ] `/ui` 엔드포인트로 접근
- [ ] 초기 비밀번호 `admin` → 첫 로그인 시 변경 강제
- [ ] 페이지:
  - **Dashboard**: 실시간 요청 수, 에러율, 레이턴시 차트
  - **Logs**: 필터링 가능한 로그 테이블 (agent, method, status, IP)
  - **Middleware**: 각 middleware on/off 토글, 설정 편집
  - **Tokens**: 토큰 목록, 발급, 폐기, 사용량 차트
  - **Settings**: YAML 설정 편집, 보안 모드 전환

---

## Phase 4 — 고도화

> 목표: 외부 평판 시스템 연동, 고급 보안 기능

### 4.1 Agent 평판 시스템

```
┌──────────┐     ┌──────────────┐     ┌─────────────────┐
│ shield-  │────▶│ Reputation   │────▶│ Web3 Registry   │
│ agent    │◀────│ Middleware   │◀────│ (on-chain score) │
└──────────┘     └──────────────┘     └─────────────────┘
```

- [ ] 로컬 평판 점수 계산 (성공률, 에러율, 패턴 분석)
- [ ] 외부 평판 서비스 조회 API 연동
- [ ] Web3 기반 agent 평판 레지스트리 (저장/조회/평가)
- [ ] 평판 기반 동적 정책 (낮은 평판 → rate limit 강화)

### 4.2 고급 보안

- [ ] HTTP MITM 프록시 (`HTTPS_PROXY` 주입, TLS 인터셉트)
- [ ] 프로토콜 자동 감지 (A2A / JSON-RPC / REST)
- [ ] 도메인/경로 기반 allow/block 규칙
- [ ] 민감 헤더 마스킹 (Authorization, Cookie)
- [ ] 양방향 신뢰 모델 (호출자 + 수신자 모두 검증)
- [ ] A2A spec agent card 기반 신원 검증
- [ ] WebSocket MCP transport 지원

### 4.3 텔레메트리 (선택적)

- [ ] 익명 usage 통계 (기본 **비활성화**)
- [ ] Differential Privacy: success 필드 flip
- [ ] Agent ID: sha256(salt + id) 해시
- [ ] IP k-anonymity: IPv4 /24, IPv6 /48 마스킹
- [ ] gzip 압축 배치 전송

---

## 코드 규칙

| 항목 | 규칙 |
|------|------|
| 언어 | Go |
| 주석 | 영어 only, 핵심만 짧게 |
| 커밋 | `type(scope): description` (Conventional Commits) |
| 브랜치 | `feat/`, `fix/`, `chore/` |
| 테스트 | 핵심/치명적 로직 필수. `_test.go` |
| 에러 | `fmt.Errorf("context: %w", err)` 패턴 |
| 로그 | `log/slog` 구조화 로깅 |
| 민감정보 | Agent ID → sha256 해시. 비밀키/토큰 절대 로그 금지 |

---

## 우선순위 요약

```
Phase 1 (Core MVP)          ★★★★★  — 핵심 기능. 이것만으로 쓸 수 있어야 함
  ├── Transport (stdio/proxy)
  ├── Auth Middleware (Ed25519/DID)
  ├── Guard Middleware (rate limit, IP, size)
  ├── Log Middleware (SQLite)
  ├── 동적 Middleware Chain (YAML)
  └── CLI (logs, config)

Phase 2 (배포/설치)          ★★★★☆  — 사용자 유입의 관문
  ├── Docker / Compose
  ├── brew / curl 설치
  ├── GoReleaser + CI/CD
  └── 문서 (EN + KO)

Phase 3 (토큰 & UI)         ★★★☆☆  — 관리 편의성
  ├── 토큰 발급/관리/quota
  └── Web UI (dashboard, logs, control)

Phase 4 (고도화)             ★★☆☆☆  — 차별화. 시장 반응 보고 결정
  ├── Agent 평판 시스템 (Web3)
  ├── 고급 보안 (MITM, protocol detect)
  └── 텔레메트리
```
