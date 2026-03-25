# shield-agent

AI Agent 시대의 보안 미들웨어. Agent와 Server 사이에 투명하게 위치하여 인증, 방어, 로깅, 모니터링을 제공합니다.

Go로 작성되었으며, 단일 바이너리로 즉시 사용 가능합니다.

```
+------------------+         +----------------------------+         +------------------+
|                  |         |       shield-agent         |         |                  |
|   AI Agent /     | ------> | +------------------------+ | ------> | Target Server    |
|   MCP Client /   |         | | Middleware Chain       | |         | (MCP / A2A /     |
|   A2A Agent      | <------ | | [auth] [guard] [log]   | | <------ |  REST API)       |
+------------------+         | +------------------------+ |         +------------------+
                             |   monitor :9090 /metrics   |
                             +----------------------------+
```

## 보호 대상

| 케이스 | 설명 | 모드 |
|--------|------|------|
| Agent → MCP Server | stdio 래핑 / HTTP 리버스 프록시 | stdio, proxy |
| Agent → Agent (A2A) | Google A2A 프로토콜 에이전트 간 통신 | HTTP 미들웨어 |
| Agent → API Server | 일반 HTTP REST/GraphQL 호출 | HTTP 미들웨어 |

## 주요 기능

### 동작 모드
- **stdio 모드** — MCP 서버를 자식 프로세스로 래핑, stdin/stdout 인터셉트
- **proxy 모드** — HTTP 리버스 프록시 (SSE / Streamable HTTP / TLS)

### 보안
- **Ed25519 인증** — 암호화 서명 기반 에이전트 신원 검증 (`did:key` + KeyStore)
- **Guard 미들웨어** — 속도 제한, 요청 크기 제한, IP 차단/허용, brute force 방어, malformed JSON-RPC 감지
- **A2A 인증** — 에이전트 간 통신 Ed25519 서명 검증 (`X-Agent-ID` / `X-A2A-Signature`)
- **HTTP API 인증** — 외부 API 호출 서명 검증 (`X-Agent-ID` / `X-Agent-Signature`)
- **프라이버시 우선 텔레메트리** — 차등 프라이버시(differential privacy) 적용, 기본 비활성화

### 관측성 (Observability)
- **감사 로깅** — 모든 요청/응답을 SQLite에 비동기 저장 (IP 주소 포함)
- **Prometheus 메트릭** — 내장 `/metrics` 엔드포인트
- **헬스 체크** — `/healthz` (자식 프로세스 / 업스트림 상태 확인)
- **로그 조회 CLI** — `shield-agent logs` 명령으로 필터링/검색

### 운영
- **동적 미들웨어 체인** — YAML 설정, CLI 오버라이드, SIGHUP 핫 리로드
- **TLS 지원** — `--tls-cert` / `--tls-key`로 HTTPS 프록시
- **DB 마이그레이션** — 자동 스키마 버전 관리
- **CI/CD** — GitHub Actions + GoReleaser 자동 빌드/배포

## 설치

### Homebrew (macOS / Linux)

```bash
brew tap itdar/tap && brew install shield-agent
```

### curl 설치 스크립트

```bash
curl -sSL https://raw.githubusercontent.com/itdar/shield-agent/main/scripts/install.sh | sh
```

### Go install

```bash
go install github.com/itdar/shield-agent/cmd/shield-agent@latest
```

### Docker

```bash
docker pull ghcr.io/itdar/shield-agent:latest
```

### 소스에서 빌드

```bash
git clone https://github.com/itdar/shield-agent.git
cd shield-agent
go build -o shield-agent ./cmd/shield-agent
```

## 빠른 시작

### stdio 모드 — MCP 서버 프로세스 래핑

```bash
shield-agent python server.py
shield-agent --verbose node server.js --port 8080
```

shield-agent는 MCP 서버를 자식 프로세스로 실행하여, 미들웨어 체인을 통해 stdin/stdout을 파이핑하고 stderr는 그대로 통과시킵니다.

### proxy 모드 — HTTP 리버스 프록시

```bash
# Streamable HTTP (기본값)
shield-agent proxy --listen :8888 --upstream http://localhost:8000

# SSE 전송 방식
shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport sse

# TLS를 사용한 HTTPS
shield-agent proxy --listen :8888 --upstream http://localhost:8000 --tls-cert cert.pem --tls-key key.pem
```

### Docker Compose

```yaml
services:
  shield:
    image: ghcr.io/itdar/shield-agent:latest
    command: proxy --listen :8888 --upstream http://mcp-server:8000
    ports:
      - "8888:8888"
      - "9090:9090"
    volumes:
      - ./shield-agent.yaml:/shield-agent.yaml:ro
      - ./keys.yaml:/keys.yaml:ro
  mcp-server:
    image: your-mcp-server:latest
```

## 동작 모드

### stdio 모드

```
shield-agent [flags] <command> [args...]
```

```
+------------+    stdin     +----------------+    stdin     +-------------+
|            | -----------> |                | -----------> |             |
| MCP Client |              |  shield-agent  |              | MCP Server  |
|            | <----------- | (middleware)   | <----------- | (child proc)|
+------------+    stdout    +----------------+    stdout    +-------------+
                                  |    ^
                             stderr passes through
                             without interception
```

- 자식 프로세스를 래핑하여 JSON-RPC 파이프라인으로서 stdin/stdout을 가로챔
- SIGINT/SIGTERM을 자식 프로세스에 전달
- 자식 프로세스의 종료 코드 전파
- SIGKILL 전 5초 graceful shutdown 타임아웃
- stderr는 가로채지 않고 그대로 통과

### Proxy 모드

```
shield-agent proxy --listen :8888 --upstream <url> --transport <sse|streamable-http>
```

```
+------------+   HTTP/HTTPS   +-------------------+   HTTP   +-------------+
|            | -------------> |                   | -------> |             |
| MCP Client |                |  shield-agent     |          | MCP Server  |
|            | <------------- |  :8888 (proxy)    | <------- | (upstream)  |
+------------+                +-------------------+          +-------------+
                                      |
                               monitor :9090
                               /healthz /metrics
```

| Flag | 기본값 | 설명 |
|------|--------|------|
| `--listen` | `:8888` | 수신 대기 주소 |
| `--upstream` | (필수) | 업스트림 서버 기본 URL |
| `--transport` | `streamable-http` | `sse` 또는 `streamable-http` |
| `--tls-cert` | — | TLS 인증서 파일 경로 |
| `--tls-key` | — | TLS 키 파일 경로 |

#### SSE 전송 방식

| 엔드포인트 | 설명 |
|-----------|------|
| `GET /sse` | 업스트림 SSE에 연결하고 이벤트를 릴레이 |
| `POST /messages?sessionId=<id>` | 미들웨어 적용 후 업스트림으로 전달 |

#### Streamable HTTP 전송 방식

| Method | Path | 설명 |
|--------|------|------|
| `POST` | `/mcp` 또는 `/` | 미들웨어 적용 후 업스트림으로 전달 |
| `GET` | `/mcp` | 세션 SSE 스트림 열기 |
| `DELETE` | `/mcp` | 세션 종료 |

## 미들웨어

### 파이프라인

모든 모드에서 동일한 설정 가능한 미들웨어 체인을 사용합니다. 기본 순서: **auth → guard → log**

```
Request 흐름:
  클라이언트 요청
       |
       v
  [auth] ----실패----> JSON-RPC 에러 응답
       |
       v
  [guard] ---실패----> JSON-RPC 에러 응답
       |
       v
  [log] (요청 기록)
       |
       v
  업스트림 서버로 전달
```

```go
type Middleware interface {
    ProcessRequest(ctx context.Context, req *Request) (*Request, error)
    ProcessResponse(ctx context.Context, resp *Response) (*Response, error)
}
```

### 동적 미들웨어 체인

YAML `middlewares` 섹션으로 설정. CLI 플래그로 개별 토글 가능. SIGHUP으로 런타임 리로드.

```yaml
middlewares:
  - name: auth
    enabled: true
  - name: guard
    enabled: true
    config:
      rate_limit_per_min: 60
      max_body_size: 65536
      ip_blocklist:
        - "203.0.113.0/24"
      ip_allowlist:
        - "10.0.0.0/8"
  - name: log
    enabled: true
```

```bash
# CLI로 미들웨어 토글
shield-agent proxy --disable-middleware guard --upstream http://localhost:8000
shield-agent proxy --enable-middleware log --upstream http://localhost:8000
```

### Auth (인증)

Ed25519 서명 기반 에이전트 인증. MCP (JSON-RPC), A2A, HTTP API 세 가지 통신 모두에 동일한 검증 로직을 적용합니다.

| 프로토콜 | Agent ID 헤더/필드 | 서명 헤더/필드 |
|---------|-------------------|---------------|
| MCP (JSON-RPC) | `_mcp_agent_id` (params) | `_mcp_signature` (params) |
| A2A | `X-Agent-ID` (header) | `X-A2A-Signature` (header) |
| HTTP API | `X-Agent-ID` (header) | `X-Agent-Signature` (header) |

**Agent ID 형식:**

| 형식 | 해석 |
|------|------|
| `did:key:z...` | Base58btc 디코드 + 멀티코덱(0xed01) 검증 |
| 일반 문자열 | `keys.yaml`에서 조회 |

**보안 모드:** `open` (경고만, 기본값) / `closed` (미인증 요청 거부)

### Guard (방어)

| Config key | 기본값 | 설명 |
|------------|--------|------|
| `rate_limit_per_min` | `0` (무제한) | 메서드별 분당 최대 요청 수 |
| `max_body_size` | `0` (무제한) | 최대 요청 본문 크기 (바이트) |
| `ip_blocklist` | — | 차단할 CIDR 범위 또는 IP |
| `ip_allowlist` | — | 허용할 CIDR 범위 (비어 있으면 모두 허용) |
| `brute_force_max_fails` | `0` (비활성) | 연속 실패 N회 시 10분 자동 차단 |
| `validate_jsonrpc` | `false` | malformed JSON-RPC 페이로드 거부 |

### Log (감사 로깅)

SQLite에 비동기 요청/응답 로깅.

- 논블로킹 write 채널 (버퍼 512)
- ID별 보류 요청 추적, 응답 시 레이턴시 계산
- Prometheus 카운터/히스토그램 연동

## 로그 조회 CLI

```bash
shield-agent logs [flags]
```

| Flag | 설명 |
|------|------|
| `--last N` | 최근 N개 항목 (기본값: 50) |
| `--agent <id>` | 에이전트 ID로 필터링 |
| `--since <duration>` | 시간 필터 (예: `1h`, `30m`) |
| `--method <name>` | JSON-RPC 메서드로 필터링 |
| `--format json\|table` | 출력 형식 (기본값: `table`) |

## 스토리지

SQLite (WAL 모드, busy timeout 5초). 기본 경로: `shield-agent.db`

- **자동 마이그레이션**: `schema_versions` 테이블로 버전 관리
- **자동 삭제**: `retention_days` (기본 30일) 경과 항목 삭제

**스키마 (`action_logs`):**

| 컬럼 | 설명 |
|------|------|
| `timestamp` | 기록 시각 |
| `agent_id_hash` | 에이전트 ID SHA-256 해시 (익명화) |
| `method` | JSON-RPC 메서드명 |
| `direction` | `in` / `out` |
| `success` | 성공 여부 |
| `latency_ms` | 레이턴시 (밀리초) |
| `payload_size` | 크기 (바이트) |
| `auth_status` | `verified` / `failed` / `unsigned` |
| `error_code` | 에러 코드 |
| `ip_address` | 요청 원본 IP |

## 모니터링

기본 주소: `127.0.0.1:9090`

| 엔드포인트 | 설명 |
|-----------|------|
| `/` | JSON 인덱스 |
| `/healthz` | 헬스 체크 (`healthy` / `degraded`) |
| `/metrics` | Prometheus 메트릭 |

### Prometheus 메트릭

| Metric | Type | Labels |
|--------|------|--------|
| `shield_agent_messages_total` | Counter | `direction`, `method` |
| `shield_agent_auth_total` | Counter | `status` |
| `shield_agent_message_latency_seconds` | Histogram | `method` |
| `shield_agent_child_process_up` | Gauge | — |
| `shield_agent_rate_limit_rejected_total` | Counter | `method` |

## 설정

**우선순위:** CLI 플래그 > 환경 변수 > YAML 설정 파일 > 기본값

`shield-agent.example.yaml`을 `shield-agent.yaml`로 복사하여 시작하세요.

| Setting | 기본값 | 환경 변수 |
|---------|--------|-----------|
| `server.monitor_addr` | `127.0.0.1:9090` | `SHIELD_AGENT_MONITOR_ADDR` |
| `server.tls_cert` | — | `SHIELD_AGENT_TLS_CERT` |
| `server.tls_key` | — | `SHIELD_AGENT_TLS_KEY` |
| `server.cors_allowed_origins` | `["*"]` | — |
| `security.mode` | `open` | `SHIELD_AGENT_SECURITY_MODE` |
| `security.key_store_path` | `keys.yaml` | `SHIELD_AGENT_KEY_STORE_PATH` |
| `logging.level` | `info` | `SHIELD_AGENT_LOG_LEVEL` |
| `logging.format` | `json` | `SHIELD_AGENT_LOG_FORMAT` |
| `storage.db_path` | `shield-agent.db` | `SHIELD_AGENT_DB_PATH` |
| `storage.retention_days` | `30` | `SHIELD_AGENT_RETENTION_DAYS` |
| `telemetry.enabled` | `false` | `SHIELD_AGENT_TELEMETRY_ENABLED` |

### 전역 CLI 플래그

| Flag | 설명 |
|------|------|
| `--config <path>` | 설정 파일 경로 (기본값: `shield-agent.yaml`) |
| `--log-level debug\|info\|warn\|error` | 로그 상세 수준 |
| `--verbose` | `--log-level debug`의 별칭 |
| `--telemetry` | 익명 텔레메트리 활성화 |
| `--monitor-addr <addr>` | 모니터링 수신 주소 |
| `--disable-middleware <name>` | 미들웨어 비활성화 |
| `--enable-middleware <name>` | 미들웨어 활성화 |

## 현재 제한 사항

- 요청 콘텐츠 필터링 없음 — 메타데이터만 기록
- 동적 키 등록 API 없음 (`keys.yaml` 수동 편집 필요)
- 텔레메트리는 별도 수집 서버 필요
- WebSocket MCP 전송 방식 미지원
- 토큰 기반 접근 제어 미구현 (Phase 3 예정)
- Web UI 미구현 (Phase 3 예정)

## 로드맵

자세한 내용은 [ROADMAP.md](ROADMAP.md)를 참고하세요.

| Phase | 상태 | 설명 |
|-------|------|------|
| Phase 1 — Core MVP | **완료** | Transport, Auth, Guard, Log, Middleware Chain, CLI |
| Phase 2 — 배포 & 설치 | **완료** | Docker, Homebrew, GoReleaser, CI/CD, 문서 |
| Phase 3 — 토큰 & Web UI | 예정 | 토큰 발급/관리/quota, Web UI 대시보드 |
| Phase 4 — 고도화 | 예정 | Agent 평판 시스템, 고급 보안, 텔레메트리 |

## 라이선스

[MIT](LICENSE)
