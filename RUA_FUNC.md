# rua (mcp-shield) — 현재 구현된 기능 목록

`mcp-shield`는 Go로 작성된 MCP 보안 미들웨어 프록시. MCP 서버와 AI 에이전트 사이에 투명하게 끼어들어 JSON-RPC 메시지를 인터셉트한다.

---

## 실행 모드 (2가지)

| 모드 | 명령 | 대상 |
|------|------|------|
| **stdio 모드** | `mcp-shield <command> [args...]` | stdio 기반 MCP 서버 프로세스 래핑 |
| **proxy 모드** | `mcp-shield proxy --upstream <url>` | HTTP 기반 MCP 서버 앞단 프록시 |

---

## 1. stdio 모드 — 프로세스 래핑

```
mcp-shield [flags] <command> [args...]
```

- 자식 프로세스(MCP 서버)를 직접 실행하고 stdin/stdout을 파이프로 연결
- 자식의 stderr는 그대로 통과 (인터셉트 없음)
- SIGINT, SIGTERM을 자식 프로세스에 포워딩
- 자식 exit code를 그대로 전파 (ExitError 그대로 반환)
- graceful shutdown 타임아웃 5초 후 SIGKILL

---

## 2. proxy 모드 — HTTP MCP 프록시

```
mcp-shield proxy --listen :8888 --upstream <url> --transport <sse|streamable-http>
```

HTTP 기반 MCP 서버 앞단에 위치하는 리버스 프록시. 동일한 미들웨어 체인(Auth → Log)이 적용된다.

### 2-1. SSE transport (`--transport sse`)

MCP SSE 스펙 구현. `internal/transport/proxy/sse.go`

| 엔드포인트 | 역할 |
|-----------|------|
| `GET /sse` | 업스트림 SSE 연결 후 이벤트 relay. endpoint 이벤트의 메시지 URL을 로컬 주소로 재작성 |
| `POST /messages?sessionId=<id>` | 요청에 미들웨어 적용 후 업스트림 `/messages` 포워딩 |

- 세션별 이벤트 채널 (버퍼 512) 관리 (`sessionStore`)
- SSE 응답 data 필드에 `ProcessResponse` 적용

### 2-2. Streamable HTTP transport (`--transport streamable-http`)

MCP Streamable HTTP 스펙 구현. `internal/transport/proxy/streamable.go`

| 메서드 | 경로 | 역할 |
|--------|------|------|
| `POST` | `/mcp` or `/` | 요청에 미들웨어 적용 후 업스트림 포워딩 |
| `GET` | `/mcp` | 세션 SSE 스트림 열기 — 미들웨어 없이 raw proxy |
| `DELETE` | `/mcp` | 세션 종료 — 미들웨어 없이 raw proxy |

- 응답이 `text/event-stream`이면 SSE 청크 단위로 relay + `ProcessResponse` 적용
- 단순 JSON 응답은 전체 읽어서 `ProcessResponse` 후 반환

### proxy 모드 CLI 플래그

| 플래그 | 기본값 | 설명 |
|--------|--------|------|
| `--listen` | `:8888` | 리슨 주소 |
| `--upstream` | (필수) | 업스트림 MCP 서버 base URL |
| `--transport` | `streamable-http` | `sse` 또는 `streamable-http` |

---

## 3. JSON-RPC 파이프라인 인터셉션 (stdio 모드)

두 방향 모두 파싱해서 미들웨어 체인을 통과시킴.

| 방향 | 처리 |
|------|------|
| stdin (상위→자식) | `PipelineIn`: 요청을 파싱 → 미들웨어 체인 → 자식에 전달 |
| stdout (자식→상위) | `PipelineOut`: 응답을 파싱 → 미들웨어 체인 → 상위에 전달 |

- JSON 파싱 실패(비-JSON)나 예상치 못한 방향의 메시지는 그대로 forward
- 요청이 미들웨어에서 거부되면 JSON-RPC 에러 응답을 상위에 직접 작성 (자식에는 전달 안 함)
- 응답이 미들웨어에서 거부되면 drop (상위에 전달 안 함)

---

## 4. 미들웨어 체인

```go
type Middleware interface {
    ProcessRequest(ctx, *Request) (*Request, error)
    ProcessResponse(ctx, *Response) (*Response, error)
}
```

- `Chain`: 등록된 미들웨어를 순서대로 실행
- 첫 에러 발생 시 즉시 중단, JSON-RPC 에러 페이로드 반환
- `PassthroughMiddleware`: no-op 임베딩용 기본 구현
- 현재 체인: **AuthMiddleware → LogMiddleware**
- stdio / proxy 모드 모두 동일한 체인 사용

---

## 5. 인증 미들웨어 (AuthMiddleware)

Ed25519 서명 기반 에이전트 인증.

### 동작 방식
- JSON-RPC 요청의 `params` 에서 `_mcp_agent_id`, `_mcp_signature` 추출
- payload = sha256({method, params without _mcp_signature}) → Ed25519 verify

### agent ID 형식 지원
| 형식 | 처리 |
|------|------|
| `did:key:z...` | Base58btc 디코딩 + multicodec(0xed01) 검증으로 pub key 추출 |
| 일반 문자열 | `keys.yaml`에서 조회 |

### 모드
- **`open`** (기본): 인증 실패해도 경고 로그만 찍고 요청 통과 (관찰 모드)
- **`closed`**: 인증 실패 시 요청 거부, JSON-RPC 에러 반환

### KeyStore
- `FileKeyStore`: YAML 파일 (`keys.yaml`) 에서 Ed25519 공개키 로드
- `CachedKeyStore`: TTL 5분 캐시 래퍼
- agent ID는 로그에 sha256 해시로만 기록 (원본 ID 비저장)

### 인증 결과 이벤트
- `verified` / `failed` / `unsigned` → Prometheus 카운터 + 로그

---

## 6. 로깅 미들웨어 (LogMiddleware)

요청/응답 쌍을 SQLite에 비동기로 저장.

- 요청 수신 시 pending map에 저장 (method, startedAt, authStatus, agentHash)
- 응답 수신 시 pending에서 매핑, 레이턴시 계산
- 비동기 write channel (버퍼 512) → 별도 goroutine이 drain
- 채널 풀이면 drop (경고 로그)
- Notification(id 없는 요청)은 별도 처리

---

## 7. SQLite 스토리지

파일: `mcp-shield.db` (기본값)

### 스키마 (`action_logs`)
| 컬럼 | 설명 |
|------|------|
| timestamp | 기록 시각 |
| agent_id_hash | sha256(agent_id), 익명화 |
| method | JSON-RPC 메서드명 |
| direction | `in` (요청) / `out` (응답) |
| success | 성공 여부 |
| latency_ms | 레이턴시 (응답 시에만) |
| payload_size | params/result 바이트 수 |
| auth_status | verified / failed / unsigned |
| error_code | 에러 코드 (있을 때만) |

- WAL 모드, busy timeout 5초
- 인덱스: timestamp, (agent_id_hash, timestamp), method
- 자동 purge: 시작 시 retention_days(기본 30일) 이전 항목 삭제

---

## 8. 로그 조회 CLI

```
mcp-shield logs [flags]
```

| 플래그 | 설명 |
|--------|------|
| `--last N` | 최근 N개 (기본 50) |
| `--agent <id>` | 특정 agent 필터 (내부적으로 sha256 변환) |
| `--since <duration>` | 기간 필터 (예: `1h`, `30m`) |
| `--method <name>` | 메서드명 필터 |
| `--format json\|table` | 출력 포맷 |

---

## 9. 모니터링 서버 (HTTP)

기본 주소: `127.0.0.1:9090`

| 엔드포인트 | 설명 |
|-----------|------|
| `/healthz` | JSON 헬스 체크. stdio 모드에서는 자식 프로세스 PID kill(0) 으로 생존 확인. status: `healthy` / `degraded` |
| `/metrics` | Prometheus 메트릭 |

### Prometheus 메트릭
| 메트릭명 | 타입 | 레이블 |
|---------|------|--------|
| `mcp_shield_messages_total` | Counter | direction, method |
| `mcp_shield_auth_total` | Counter | status |
| `mcp_shield_message_latency_seconds` | Histogram | method |
| `mcp_shield_child_process_up` | Gauge | - (stdio 모드만 유효) |

---

## 10. 텔레메트리 수집기

익명 usage 통계. 기본 **비활성화**.

- 링 버퍼 10,000개
- 배치 주기적 전송 (기본 60초, gzip 압축)
- Differential Privacy: success 필드를 `1/(1+e^epsilon)` 확률로 flip
- agent ID: sha256(salt + id) 해시
- IP K-anonymity: IPv4 → /24 마스킹, IPv6 → /48 마스킹
- 엔드포인트: `POST {endpoint}/telemetry/ingest`

---

## 11. 설정 시스템

우선순위: **CLI 플래그 > 환경변수 > YAML 파일 > 기본값**

### 주요 설정 항목
| 항목 | 기본값 | 환경변수 |
|------|--------|---------|
| monitor_addr | `127.0.0.1:9090` | `MCP_SHIELD_MONITOR_ADDR` |
| security.mode | `open` | `MCP_SHIELD_SECURITY_MODE` |
| security.key_store_path | `keys.yaml` | `MCP_SHIELD_KEY_STORE_PATH` |
| logging.level | `info` | `MCP_SHIELD_LOG_LEVEL` |
| logging.format | `json` | `MCP_SHIELD_LOG_FORMAT` |
| storage.db_path | `mcp-shield.db` | `MCP_SHIELD_DB_PATH` |
| storage.retention_days | `30` | `MCP_SHIELD_RETENTION_DAYS` |
| telemetry.enabled | `false` | `MCP_SHIELD_TELEMETRY_ENABLED` |

### 전역 CLI 플래그 (모든 서브커맨드 공통)
- `--config <path>` (기본: `mcp-shield.yaml`)
- `--log-level debug|info|warn|error`
- `--verbose` (= `--log-level debug`)
- `--telemetry`
- `--monitor-addr <addr>`

---

## 현재 한계

- 요청 내용(content) 필터링 없음 — 메타데이터만 기록
- 동적 key 등록 API 없음 (파일 수동 편집 필요)
- 텔레메트리 수신 서버 별도 필요 (ripe가 담당)
- proxy 모드에서 `child_process_up` 메트릭 미지원 (HTTP 프록시에는 자식 프로세스 개념 없음)
- WebSocket MCP transport 미지원

---

## 12. A2A 미들웨어 (`internal/middleware/a2a/`)

에이전트 간(A2A) HTTP 통신에 대한 인증 및 로깅 미들웨어.

### 미들웨어 인터페이스

```go
type Middleware interface {
    WrapHandler(next http.Handler) http.Handler
}
```

- `Chain`: 등록된 미들웨어를 순서대로 래핑 (first = outermost)
- `responseWriter`: 상태 코드 및 바이트 수 캡처

### A2A AuthMiddleware

Ed25519 서명 기반 에이전트 인증. 헤더: `X-Agent-ID`, `X-A2A-Signature`

- payload = sha256(method + " " + path + "\n" + body) → Ed25519 verify
- `did:key:` URI 및 KeyStore 조회 지원
- **open 모드**: 인증 실패 시 경고 로그만, 요청 통과
- **closed 모드**: 인증 실패 시 HTTP 401 반환
- `onAuth` 콜백으로 "verified" / "failed" / "unsigned" 이벤트 전파

### A2A LogMiddleware

요청/응답 쌍을 SQLite에 비동기 저장.

- A2A JSON-RPC body에서 method 필드 추출
- 비동기 write channel (버퍼 512) → 별도 goroutine drain
- 텔레메트리 Recorder 연동 (선택)
- 채널 풀이면 drop (경고 로그)

---

## 13. HTTP API 미들웨어 (`internal/middleware/httpapi/`)

에이전트가 외부 HTTP API를 호출할 때의 인증 및 로깅 미들웨어.

### 미들웨어 인터페이스

A2A와 동일한 `Middleware` / `Chain` 패턴.

### HTTP API AuthMiddleware

Ed25519 서명 기반 에이전트 인증. 헤더: `X-Agent-ID`, `X-Agent-Signature`

- payload = sha256(method + " " + path + "\n" + body) → Ed25519 verify
- `did:key:` URI 및 KeyStore 조회 지원
- **open / closed 모드** 동일
- `onAuth` 콜백으로 이벤트 전파

### HTTP API LogMiddleware

요청/응답 쌍을 SQLite에 비동기 저장.

- method 라벨: `"METHOD /path"` 형식 (예: `GET /api/v1/repos`)
- 비동기 write channel (버퍼 512) → 별도 goroutine drain
- 텔레메트리 Recorder 연동 (선택)

---

## 추후 구현 예정

### A. A2A 프록시 transport 및 고급 기능

미들웨어 레이어(Auth + Log)는 구현 완료. 아래는 미구현 잔여 항목:

- A2A HTTP 프록시 transport — 에이전트가 `HTTPS_PROXY=mcp-shield-proxy`로 트래픽을 라우팅
- 프로토콜 감지: Content-Type, 요청 구조로 A2A / JSON-RPC / REST 자동 구분
- 의도(intent) 검증: 허용된 에이전트 ID 조합, 허용된 action 타입 화이트리스트
- 양방향 신뢰 모델: 호출하는 에이전트와 받는 에이전트 모두 검증
- A2A spec의 agent card 기반 신원 검증

### B. HTTP API 프록시 transport 및 고급 기능

미들웨어 레이어(Auth + Log)는 구현 완료. 아래는 미구현 잔여 항목:

- HTTP MITM 프록시 모드 — 에이전트 실행 환경에 `HTTP_PROXY` / `HTTPS_PROXY` 주입
- TLS 인터셉트를 위한 CA 인증서 생성 및 신뢰 등록
- 도메인/경로 기반 허용/차단 규칙
- 민감 헤더(Authorization, Cookie) 마스킹 후 저장
- 에이전트 신원 연결: HTTP 호출이 어떤 에이전트에서 나왔는지 추적
