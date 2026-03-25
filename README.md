# shield-agent

[MCP (Model Context Protocol)](https://modelcontextprotocol.io/) 서버를 위한 보안 미들웨어 프록시로, Go로 작성되었습니다. shield-agent는 AI 에이전트와 MCP 서버 사이에 투명하게 위치하여, JSON-RPC 메시지를 가로채 인증, 로깅, 관측성(observability) 기능을 제공합니다.

```
+------------------+         +----------------------------+         +---------------+
|                  |         |       shield-agent         |         |               |
|   AI Agent /     | ------> | +------------------------+ | ------> | Target MCP    |
|   MCP Client     |         | | Middleware Chain        | |         | Server        |
|                  | <------ | | [auth] [guard] [log]   | | <------ |               |
+------------------+         | +------------------------+ |         +---------------+
                             |   monitor :9090 /metrics   |
                             +----------------------------+
```

## 주요 기능

- **두 가지 동작 모드** — stdio 프로세스 래핑 및 HTTP 리버스 프록시
- **Ed25519 인증** — 암호화 서명을 통한 에이전트 신원 검증
- **Guard 미들웨어** — 속도 제한, 요청 크기 제한, IP 차단/허용 목록
- **감사 로깅** — 모든 요청/응답 쌍을 SQLite에 영구 저장
- **Prometheus 메트릭** — 모니터링을 위한 내장 `/metrics` 엔드포인트
- **동적 미들웨어 체인** — YAML로 설정된 파이프라인, SIGHUP을 통한 핫 리로드 지원
- **TLS 지원** — `--tls-cert` / `--tls-key`를 사용한 HTTPS 프록시
- **프라이버시 우선 텔레메트리** — 차등 프라이버시(differential privacy)를 적용한 선택적 익명 사용 통계
- **MCP 전송 지원** — SSE 및 Streamable HTTP 프록시 전송 방식
- **A2A & HTTP API 미들웨어** — 에이전트 간 통신 및 에이전트-API 통신을 위한 재사용 가능한 인증/로깅 미들웨어

## 빠른 시작

### 설치

```bash
go install github.com/itdar/shield-agent/cmd/shield-agent@latest
```

또는 소스에서 빌드:

```bash
git clone https://github.com/itdar/shield-agent.git
cd shield-agent
go build -o shield-agent ./cmd/shield-agent
```

### stdio 모드 — MCP 서버 프로세스 래핑

```bash
shield-agent python server.py
shield-agent --verbose node server.js --port 8080
```

shield-agent는 MCP 서버를 자식 프로세스로 실행하여, 미들웨어 체인을 통해 stdin/stdout을 파이핑하고 stderr는 그대로 통과시킵니다.

### Proxy 모드 — HTTP 리버스 프록시

```bash
# Streamable HTTP (기본값)
shield-agent proxy --listen :8888 --upstream http://localhost:8000

# SSE 전송 방식
shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport sse

# TLS를 사용한 HTTPS
shield-agent proxy --listen :8888 --upstream http://localhost:8000 --tls-cert cert.pem --tls-key key.pem
```

프록시는 HTTP 기반 MCP 서버에도 동일한 미들웨어 체인을 적용합니다.

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
| `--upstream` | (필수) | 업스트림 MCP 서버 기본 URL |
| `--transport` | `streamable-http` | `sse` 또는 `streamable-http` |
| `--tls-cert` | — | TLS 인증서 파일 경로 (`--tls-key`와 함께 HTTPS 활성화) |
| `--tls-key` | — | TLS 키 파일 경로 |

#### SSE 전송 방식

| 엔드포인트 | 설명 |
|-----------|------|
| `GET /sse` | 업스트림 SSE에 연결하고 이벤트를 릴레이하며, 엔드포인트 URL을 로컬 주소로 재작성 |
| `POST /messages?sessionId=<id>` | 미들웨어 적용 후 업스트림 `/messages`로 전달 |

#### Streamable HTTP 전송 방식

| Method | Path | 설명 |
|--------|------|------|
| `POST` | `/mcp` 또는 `/` | 미들웨어 적용 후 업스트림으로 전달 |
| `GET` | `/mcp` | 세션 SSE 스트림 열기 (미들웨어 없는 원시 프록시) |
| `DELETE` | `/mcp` | 세션 종료 (미들웨어 없는 원시 프록시) |

## 미들웨어

### 파이프라인

두 모드 모두 설정 가능한 미들웨어 체인을 사용합니다. 기본 순서는 **auth → guard → log** 입니다.

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

Response 흐름:
  업스트림 서버 응답
       |
       v
  [log] (응답 기록, 레이턴시 계산)
       |
       v
  클라이언트로 전달
```

```go
type Middleware interface {
    ProcessRequest(ctx context.Context, req *Request) (*Request, error)
    ProcessResponse(ctx context.Context, resp *Response) (*Response, error)
}
```

- 미들웨어는 등록 순서대로 실행됨
- 첫 번째 에러가 발생하면 체인이 중단되고 JSON-RPC 에러를 반환
- 차단된 요청은 호출자에게 에러 응답을 생성 (서버로 전달되지 않음)
- 차단된 응답은 드롭됨 (호출자에게 전달되지 않음)
- JSON 형식이 아니거나 예상치 못한 메시지는 그대로 전달

### 동적 미들웨어 체인

파이프라인은 YAML의 `middlewares` 섹션을 통해 설정합니다. 섹션을 생략하면 기본값을 사용합니다.

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

YAML을 수정하지 않고도 CLI 플래그로 개별 미들웨어를 토글할 수 있습니다:

```bash
shield-agent proxy --disable-middleware guard --upstream http://localhost:8000
shield-agent proxy --enable-middleware log --upstream http://localhost:8000
```

실행 중인 프록시에 **SIGHUP** 신호를 보내면 프로세스를 재시작하지 않고도 설정 파일, 키 스토어, 미들웨어 체인을 리로드합니다.

### 인증 (AuthMiddleware)

Ed25519 서명 기반 에이전트 인증.

**동작 방식:**
1. JSON-RPC 요청 `params`에서 `_mcp_agent_id`와 `_mcp_signature`를 추출
2. `sha256(json({method, params without _mcp_signature}))`를 계산
3. 에이전트의 공개 키로 Ed25519 서명을 검증

**에이전트 ID 형식:**

| 형식 | 해석 방법 |
|------|----------|
| `did:key:z...` | Base58btc 디코드 + 멀티코덱(0xed01) 검증 |
| 일반 문자열 | `keys.yaml`에서 조회 |

**보안 모드:**

| 모드 | 동작 |
|------|------|
| `open` (기본값) | 인증 실패 시 경고를 로깅하지만 요청을 통과시킴 (관찰 모드) |
| `closed` | 미인증 요청을 JSON-RPC 에러로 거부 |

**키 스토어:**
- `FileKeyStore` — YAML 파일(`keys.yaml`)에서 Ed25519 공개 키를 로드
- `CachedKeyStore` — 5분 TTL 캐시로 임의의 KeyStore를 래핑
- 에이전트 ID는 SHA-256 해시로만 로깅 (평문으로 저장되지 않음)

### Guard (GuardMiddleware)

속도 제한, 요청 크기 제한, IP 기반 접근 제어를 시행합니다.

| Config key | 기본값 | 설명 |
|------------|--------|------|
| `rate_limit_per_min` | `0` (무제한) | JSON-RPC 메서드별 분당 최대 요청 수 |
| `max_body_size` | `0` (무제한) | 최대 요청 본문 크기 (바이트) |
| `ip_blocklist` | — | 차단할 CIDR 범위 또는 IP |
| `ip_allowlist` | — | 허용할 CIDR 범위 또는 IP (비어 있으면 모두 허용) |

거부된 요청은 `shield_agent_rate_limit_rejected_total` Prometheus 카운터를 증가시킵니다.

### 로깅 (LogMiddleware)

SQLite에 비동기 요청/응답 로깅.

- ID별로 보류 중인 요청을 추적하고, 응답 시 레이턴시를 계산
- 백그라운드 writer 고루틴이 있는 논블로킹 쓰기 채널 (버퍼 크기 512)
- 채널이 가득 차면 경고와 함께 항목 드롭
- 알림(ID 없는 요청)은 즉시 로깅

## 로그 조회 CLI

```bash
shield-agent logs [flags]
```

| Flag | 설명 |
|------|------|
| `--last N` | 가장 최근 N개 항목 표시 (기본값: 50) |
| `--agent <id>` | 에이전트 ID로 필터링 (내부적으로 해시 처리됨) |
| `--since <duration>` | 시간 필터 (예: `1h`, `30m`) |
| `--method <name>` | JSON-RPC 메서드로 필터링 |
| `--format json\|table` | 출력 형식 (기본값: `table`) |

## 스토리지

SQLite 데이터베이스 (기본값: `shield-agent.db`), WAL 모드 및 5초 busy 타임아웃 사용.

**스키마 (`action_logs`):**

| 컬럼 | 설명 |
|------|------|
| `timestamp` | 기록 시각 |
| `agent_id_hash` | 에이전트 ID의 SHA-256 해시 (익명화) |
| `method` | JSON-RPC 메서드명 |
| `direction` | `in` (요청) / `out` (응답) |
| `success` | 호출 성공 여부 |
| `latency_ms` | 레이턴시 (밀리초, 응답에만 해당) |
| `payload_size` | params/result의 크기 (바이트) |
| `auth_status` | `verified` / `failed` / `unsigned` |
| `error_code` | 에러 코드 (있는 경우) |

**인덱스:** `timestamp`, `(agent_id_hash, timestamp)`, `method`

**자동 삭제:** 시작 시 `retention_days` (기본값: 30)보다 오래된 항목을 삭제합니다.

## 모니터링

기본 주소: `127.0.0.1:9090`

| 엔드포인트 | 설명 |
|-----------|------|
| `/` | 사용 가능한 엔드포인트 목록을 담은 JSON 인덱스 |
| `/healthz` | 헬스 체크 — stdio 모드에서는 kill(0)으로 자식 프로세스 활성 여부를 확인하고, 프록시 모드에서는 업스트림 서버도 프로브합니다. `healthy` 또는 `degraded` 반환 |
| `/metrics` | Prometheus 메트릭 |

### Prometheus 메트릭

| Metric | Type | Labels |
|--------|------|--------|
| `shield_agent_messages_total` | Counter | `direction`, `method` |
| `shield_agent_auth_total` | Counter | `status` |
| `shield_agent_message_latency_seconds` | Histogram | `method` |
| `shield_agent_child_process_up` | Gauge | — (stdio 모드 전용) |
| `shield_agent_rate_limit_rejected_total` | Counter | `method` |

## 텔레메트리

선택적 익명 사용 통계. **기본적으로 비활성화.**

- 10,000개 이벤트의 링 버퍼
- 주기적 배치 전송 (기본값: 60초마다, gzip 압축)
- **차등 프라이버시(Differential privacy):** `1/(1+e^epsilon)` 확률로 `success` 필드를 반전
- **에이전트 ID 익명화:** `sha256(salt + id)`
- **IP k-익명성:** IPv4는 /24로, IPv6는 /48로 마스킹
- 엔드포인트: `POST {endpoint}/telemetry/ingest`

## 설정

**우선순위:** CLI 플래그 > 환경 변수 > YAML 설정 파일 > 기본값

시작하려면 `shield-agent.example.yaml`을 `shield-agent.yaml`로 복사하세요.

### 설정 참조

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
| `--monitor-addr <addr>` | 모니터링 HTTP 수신 대기 주소 |
| `--disable-middleware <name>` | 시작 시 지정된 미들웨어 비활성화 |
| `--enable-middleware <name>` | 시작 시 지정된 미들웨어 활성화 |

## A2A 미들웨어

에이전트 간(A2A) HTTP 통신을 위한 재사용 가능한 인증 및 로깅 미들웨어. `internal/middleware/a2a/`에 위치합니다. 인증 로직은 `internal/middleware/httpauth` 패키지를 통해 HTTP API 미들웨어와 공유됩니다.

```go
type Middleware interface {
    WrapHandler(next http.Handler) http.Handler
}
```

**인증 헤더:** `X-Agent-ID`, `X-A2A-Signature`

- 서명 페이로드: `sha256(method + " " + path + "\n" + body)`
- `did:key:` URI 및 KeyStore 조회 지원
- Open/closed 모드 동작이 MCP 미들웨어와 동일
- 이벤트 전파를 위한 `onAuth` 콜백 (`verified` / `failed` / `unsigned`)

**Log 미들웨어:** 요청/응답 쌍을 SQLite에 비동기로 기록 (버퍼 512), 선택적 텔레메트리 전달 지원. A2A 요청 본문에서 JSON-RPC 메서드를 추출합니다.

## HTTP API 미들웨어

에이전트에서 외부 API로의 HTTP 호출을 위한 재사용 가능한 인증 및 로깅 미들웨어. `internal/middleware/httpapi/`에 위치합니다. `internal/middleware/httpauth` 패키지를 통해 A2A 미들웨어와 서명 검증 및 키 해석 로직을 공유합니다.

A2A와 동일한 `Middleware` / `Chain` 패턴 사용.

**인증 헤더:** `X-Agent-ID`, `X-Agent-Signature`

- 서명 및 검증 방식이 A2A 미들웨어와 동일
- Open/closed 모드 동작이 동일
- 메서드 레이블 형식: `"METHOD /path"` (예: `GET /api/v1/repos`)
- 선택적 텔레메트리 전달을 지원하는 비동기 로깅

## 현재 제한 사항

- 요청 콘텐츠 필터링 없음 — 메타데이터만 기록
- 동적 키 등록 API 없음 (`keys.yaml` 수동 편집 필요)
- 텔레메트리는 별도의 수집 서버 필요
- `shield_agent_child_process_up` 메트릭은 프록시 모드에서 적용 불가
- WebSocket MCP 전송 방식 미지원

## 로드맵

### A2A 프록시 전송
- `HTTPS_PROXY`를 통해 에이전트 트래픽을 라우팅하는 HTTP 프록시 전송
- 프로토콜 자동 감지 (A2A / JSON-RPC / REST)
- 에이전트 ID 및 액션 타입 화이트리스팅을 통한 인텐트 검증
- 양방향 신뢰 모델
- 에이전트 카드 기반 신원 검증 (A2A spec)

### HTTP API 프록시 전송
- `HTTP_PROXY` / `HTTPS_PROXY` 주입을 통한 HTTP MITM 프록시 모드
- CA 인증서 생성을 통한 TLS 인터셉션
- 도메인/경로 기반 허용/차단 규칙
- 민감한 헤더 마스킹 (Authorization, Cookie)
- 아웃바운드 HTTP 호출에 대한 에이전트 신원 추적

## 라이선스

[MIT](LICENSE)
