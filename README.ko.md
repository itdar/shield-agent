# shield-agent

AI Agent 시대의 보안 미들웨어.
Agent와 Server 사이에 투명하게 위치하여 **인증, 방어, 로깅, 모니터링**을 제공합니다.

Go로 작성된 **~10MB 단일 바이너리**. 설치 30초, 설정 1분.

```
┌──────────┐         ┌──────────────────────────┐         ┌──────────────┐
│ AI Agent │ ──────> │      shield-agent        │ ──────> │ Target Server│
│ MCP/A2A  │ <────── │  [auth] [guard] [log]    │ <────── │ MCP/A2A/API  │
└──────────┘         │  monitor :9090 /metrics  │         └──────────────┘
                     │  Web UI  :9090 /ui       │
                     └──────────────────────────┘
```

🌐 [English](README.md) | [한국어](README.ko.md) | [日本語](README.ja.md) | [中文](README.zh.md)

---

## 목차

- [어떤 상황에서 쓰나요?](#어떤-상황에서-쓰나요)
- [설치](#설치)
- [사용 케이스별 빠른 시작](#사용-케이스별-빠른-시작)
- [인증 방식 선택 가이드](#인증-방식-선택-가이드)
- [프로토콜 자동 감지](#프로토콜-자동-감지)
- [배포 패턴](#배포-패턴)
- [설정 레퍼런스](#설정-레퍼런스)
- [모니터링](#모니터링)
- [Web UI](#web-ui)
- [Agent 평판 시스템](#agent-평판-시스템)
- [로드맵](#로드맵)

---

## 어떤 상황에서 쓰나요?

| 상황 | shield-agent가 해주는 것 |
|------|------------------------|
| MCP 서버를 외부에 공개하는데 아무나 호출하면 안 됨 | Ed25519 서명 / 토큰 기반 인증 |
| 에이전트가 API를 너무 많이 호출함 | Rate limit + 시간당/월간 쿼터 |
| 누가 언제 뭘 호출했는지 기록이 필요함 | SQLite 감사 로그 + Prometheus 메트릭 |
| 특정 IP에서 오는 요청을 차단하고 싶음 | IP blocklist/allowlist |
| 여러 MCP 서버를 하나의 엔드포인트로 묶고 싶음 | Gateway 모드 (Host/Path 기반 라우팅) |
| Web UI로 편하게 관리하고 싶음 | 내장 대시보드 `/ui` |

### 보호 대상

| 통신 경로 | 프로토콜 | 모드 |
|----------|---------|------|
| Agent → MCP Server | JSON-RPC (stdio / SSE / Streamable HTTP) | stdio, proxy |
| Agent → Agent (A2A) | Google A2A 프로토콜 | HTTP 미들웨어 |
| Agent → API Server | REST / GraphQL | HTTP 미들웨어 |

---

## 설치

```bash
# Homebrew (macOS / Linux)
brew tap itdar/tap && brew install shield-agent

# curl 설치 스크립트
curl -sSL https://raw.githubusercontent.com/itdar/shield-agent/main/scripts/install.sh | sh

# Go install
go install github.com/itdar/shield-agent/cmd/shield-agent@latest

# Docker
docker pull ghcr.io/itdar/shield-agent:latest

# 소스 빌드
git clone https://github.com/itdar/shield-agent.git
cd shield-agent && go build -o shield-agent ./cmd/shield-agent
```

---

## 사용 케이스별 빠른 시작

### 케이스 1: 로컬 MCP 서버 보호 (stdio 모드)

> Python/Node.js MCP 서버를 감싸서 인증과 로깅을 추가하고 싶을 때

```bash
# 가장 간단한 사용법 — MCP 서버 프로세스를 감싸기만 하면 됨
shield-agent python my_mcp_server.py

# verbose 모드로 디버깅
shield-agent --verbose node server.js --port 8080
```

**동작 원리:**
```
MCP Client ──stdin──> shield-agent ──stdin──> MCP Server (child process)
MCP Client <─stdout── shield-agent <─stdout── MCP Server
                          │
                     미들웨어 체인
                   [auth] [guard] [log]
```

- shield-agent가 MCP 서버를 자식 프로세스로 실행
- stdin/stdout을 가로채서 미들웨어 체인 적용
- stderr는 그대로 통과
- SIGINT/SIGTERM 자동 전달, 종료 코드 전파

### 케이스 2: 원격 MCP 서버 앞에 프록시 (proxy 모드)

> 이미 돌아가고 있는 MCP 서버 앞에 보안 레이어를 추가하고 싶을 때

```bash
# Streamable HTTP (기본)
shield-agent proxy --listen :8888 --upstream http://localhost:8000

# SSE 전송 방식
shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport sse

# HTTPS
shield-agent proxy --listen :8888 --upstream http://localhost:8000 \
  --tls-cert cert.pem --tls-key key.pem
```

**동작 원리:**
```
MCP Client ──HTTP──> shield-agent :8888 ──HTTP──> MCP Server :8000
                          │
                     미들웨어 체인
                     모니터링 :9090
```

### 케이스 3: 여러 서버를 하나의 Gateway로 (Gateway 모드)

> 여러 MCP/API 서버들을 하나의 shield-agent 엔드포인트 뒤에 두고 싶을 때

`shield-agent.yaml`:
```yaml
upstreams:
  - name: mcp-server-a
    url: http://10.0.1.1:8000
    match:
      host: mcp-a.example.com       # Host 헤더 기반 라우팅
    transport: sse

  - name: api-server-b
    url: http://10.0.2.1:3000
    match:
      path_prefix: /api-b           # Path 기반 라우팅
      strip_prefix: true

  - name: default-mcp
    url: http://10.0.3.1:8000       # 매칭 안 되면 여기로 (fallback)
```

```bash
shield-agent proxy --listen :8888
```

**동작 원리:**
```
Agent A ──mcp-a.example.com──> shield-agent :8888 ──> MCP Server A
Agent B ──/api-b/v1/data─────> shield-agent :8888 ──> API Server B (/v1/data)
Agent C ──기타 요청──────────> shield-agent :8888 ──> Default MCP
```

- Host 헤더 또는 URL path prefix로 라우팅
- 매칭 우선순위: Host+Path > Host만 > Path만 > fallback
- `strip_prefix: true`면 upstream에는 prefix 제거 후 전달
- Web UI (`/ui`)에서 upstream 동적 추가/삭제 가능

### 케이스 4: Docker Compose로 배포

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
      - shield-data:/data
    environment:
      - SHIELD_AGENT_DB_PATH=/data/shield-agent.db

  mcp-server:
    image: your-mcp-server:latest

volumes:
  shield-data:
```

### 케이스 5: 토큰으로 에이전트 접근 제어

> 외부 에이전트에게 API 키처럼 토큰을 발급하고, 쿼터를 관리하고 싶을 때

```bash
# 토큰 발급
shield-agent token create --name "partner-agent" --quota-hourly 1000
# → Token: a3f8c1...  (이 값은 한 번만 보여짐, 안전하게 보관)

# 토큰 목록 확인
shield-agent token list

# 사용량 확인
shield-agent token stats <token-id> --since 24h

# 토큰 폐기 (즉시 효력)
shield-agent token revoke <token-id>
```

에이전트는 요청 시 헤더에 토큰 포함:
```
Authorization: Bearer a3f8c1...
```

---

## 인증 방식 선택 가이드

shield-agent는 **3가지 인증 방식**을 지원합니다. 상황에 맞게 선택하세요:

```
┌─────────────────────────────────────────────────────────────────┐
│ Ed25519 + keys.yaml    — 고보안, 소수 에이전트                    │
│ Ed25519 + DID          — 오픈 생태계, 사전 등록 불필요             │
│ Bearer Token           — 간편, API 키처럼, 쿼터 관리 가능          │
└─────────────────────────────────────────────────────────────────┘
```

| | Ed25519 + keys.yaml | Ed25519 + DID | Bearer Token |
|---|---|---|---|
| **누가 키를 만드나** | 에이전트가 생성, 관리자가 등록 | 에이전트가 생성, 등록 불필요 | 관리자가 발급 |
| **사전 등록** | 필요 (keys.yaml 또는 Web UI) | 불필요 | 토큰 전달 필요 |
| **요청마다 서명** | O | O | X |
| **토큰 탈취 위험** | 개인키 탈취 필요 (어려움) | 개인키 탈취 필요 | 토큰 탈취하면 끝 |
| **쿼터/만료** | X | X | O (시간당/월간) |
| **즉시 폐기** | keys.yaml에서 삭제 | blocklist에 추가 | `token revoke` 즉시 |
| **추천 상황** | 내부 에이전트 5~10개 | 불특정 다수, 오픈 생태계 | 외부 파트너, API 키처럼 |

### keys.yaml 등록 방식

```yaml
# keys.yaml
keys:
  - id: "agent-1"
    key: "base64로인코딩된Ed25519공개키"
```

에이전트 요청 시 JSON-RPC params에 포함:
```json
{
  "method": "tools/list",
  "params": {
    "_mcp_agent_id": "agent-1",
    "_mcp_signature": "hex인코딩된서명값"
  }
}
```

### Web UI로 키 등록 (keys.yaml 대신)

Web UI `/ui`에서 **Agent Keys** 메뉴로 공개키 등록/삭제 가능.
keys.yaml과 DB 양쪽 모두에서 키를 찾으므로, 어느 쪽에 등록해도 동작합니다.

### DID 방식 (사전 등록 불필요)

에이전트가 `did:key:z6Mk...` 형식의 ID를 사용하면 ID 자체에서 공개키를 추출합니다.
keys.yaml 등록이 필요 없어서 대규모 에이전트 환경에 적합합니다.

### 보안 모드

| 모드 | 동작 |
|------|------|
| `open` (기본) | 서명 없어도 통과, 경고만 로깅 |
| `verified` | 유효한 서명 필수, 미등록 DID도 OK (단, rate limit 차등 가능) |
| `closed` | 등록된 에이전트만 접근 가능 |

```yaml
# shield-agent.yaml
security:
  mode: verified
  did_blocklist:            # 악의적 DID만 차단 (allowlist 아님)
    - "did:key:z6Mk..."
```

---

## 프로토콜 자동 감지

shield-agent는 각 요청의 통신 프로토콜을 자동으로 감지합니다:

| 프로토콜 | 감지 신호 |
|----------|----------|
| **MCP** (JSON-RPC 2.0) | `Mcp-Session-Id` 헤더, 또는 A2A가 아닌 메서드의 JSON-RPC |
| **A2A** (Google Agent-to-Agent) | `X-A2A-Signature` 헤더, 또는 `tasks/*` 메서드의 JSON-RPC |
| **HTTP API** (REST/GraphQL) | JSON-RPC 구조 없음 |

기본적으로 감지는 자동으로 이루어집니다. upstream별로 프로토콜 힌트를 직접 설정할 수도 있습니다:

```yaml
upstreams:
  - name: mcp-server
    url: http://10.0.1.1:8000
    protocol: mcp        # skip detection, always MCP

  - name: a2a-agent
    url: http://10.0.2.1:3000
    protocol: a2a        # skip detection, always A2A

  - name: mixed
    url: http://10.0.3.1:4000
    protocol: auto       # default: detect per request
```

---

## 배포 패턴

### 패턴 1: 사이드카 (서버마다 1개)

```
[Agent] → [shield-agent :8881] → [MCP Server A]
[Agent] → [shield-agent :8882] → [MCP Server B]
```

- 가장 단순한 구조
- 서비스 2~3개일 때 추천
- 각 shield-agent가 독립적으로 인증/로깅

### 패턴 2: Gateway (중앙 1개)

```
[Agent A] ──> [shield-agent :8888] ──> [MCP Server A]
[Agent B] ──>       (gateway)     ──> [API Server B]
```

- 서비스 5개 이상일 때 추천
- 인증/토큰/로그를 한 곳에서 관리
- `upstreams` 설정으로 Host/Path 라우팅

### 패턴 3: nginx + shield-agent

```
[Agent] → [nginx (TLS)] → [shield-agent :8888 (HTTP)] → [upstream]
```

- nginx에서 TLS termination 처리
- shield-agent는 HTTP로만 동작
- `--tls-cert/--tls-key` 설정 불필요

### 모니터링 통합 (사이드카에서도 중앙 관측)

```
[shield-agent A :9090/metrics] ──┐
[shield-agent B :9091/metrics] ──┼── Prometheus ──> Grafana
[shield-agent C :9092/metrics] ──┘
```

Prometheus가 각 shield-agent의 `/metrics`를 scrape하면
사이드카 구조에서도 전체 서비스의 요청 수/에러율/레이턴시를 중앙에서 볼 수 있습니다.

---

## 설정 레퍼런스

**우선순위:** CLI 플래그 > 환경 변수 (`SHIELD_AGENT_*`) > YAML 설정 > 기본값

`shield-agent.example.yaml`을 `shield-agent.yaml`로 복사하여 시작하세요.

### 주요 설정

| 설정 | 기본값 | 환경 변수 | 설명 |
|------|--------|-----------|------|
| `security.mode` | `open` | `SHIELD_AGENT_SECURITY_MODE` | `open` / `verified` / `closed` |
| `security.key_store_path` | `keys.yaml` | `SHIELD_AGENT_KEY_STORE_PATH` | 공개키 파일 경로 |
| `security.did_blocklist` | `[]` | — | 차단할 DID 목록 |
| `storage.db_path` | `shield-agent.db` | `SHIELD_AGENT_DB_PATH` | SQLite DB 경로 |
| `storage.retention_days` | `30` | `SHIELD_AGENT_RETENTION_DAYS` | 로그 보관 일수 |
| `server.monitor_addr` | `127.0.0.1:9090` | `SHIELD_AGENT_MONITOR_ADDR` | 모니터링/UI 주소 |
| `logging.level` | `info` | `SHIELD_AGENT_LOG_LEVEL` | `debug`/`info`/`warn`/`error` |
| `logging.format` | `json` | `SHIELD_AGENT_LOG_FORMAT` | `json` / `text` |

### 미들웨어 설정

```yaml
middlewares:
  - name: auth
    enabled: true
  - name: guard
    enabled: true
    config:
      rate_limit_per_min: 60          # 메서드별 분당 요청 제한
      max_body_size: 65536            # 최대 요청 크기 (바이트)
      ip_blocklist: ["203.0.113.0/24"]
      ip_allowlist: ["10.0.0.0/8"]    # 비어있으면 모두 허용
      brute_force_max_fails: 5        # 연속 5회 실패 시 자동 차단
      validate_jsonrpc: true          # malformed JSON-RPC 거부
  - name: token
    enabled: false                    # 토큰 미들웨어 (필요시 활성화)
  - name: log
    enabled: true
```

### CLI 플래그

| 플래그 | 설명 |
|--------|------|
| `--config <path>` | 설정 파일 경로 (기본: `shield-agent.yaml`) |
| `--log-level <level>` | 로그 수준 |
| `--verbose` | `--log-level debug` 별칭 |
| `--monitor-addr <addr>` | 모니터링 주소 |
| `--disable-middleware <name>` | 미들웨어 비활성화 |
| `--enable-middleware <name>` | 미들웨어 활성화 |

### SIGHUP 핫 리로드

프로세스 재시작 없이 설정 변경:

```bash
kill -HUP $(pgrep shield-agent)
```

미들웨어 체인, keys.yaml, DID blocklist가 리로드됩니다.

---

## 모니터링

### 엔드포인트

| 경로 | 설명 |
|------|------|
| `/healthz` | 헬스 체크 (`healthy` / `degraded`) |
| `/metrics` | Prometheus 메트릭 |
| `/ui` | Web UI 대시보드 |

### Prometheus 메트릭

| 메트릭 | 타입 | 라벨 |
|--------|------|------|
| `shield_agent_messages_total` | Counter | `direction`, `method` |
| `shield_agent_auth_total` | Counter | `status` |
| `shield_agent_message_latency_seconds` | Histogram | `method` |
| `shield_agent_child_process_up` | Gauge | — |
| `shield_agent_rate_limit_rejected_total` | Counter | `method` |

### 로그 조회 CLI

```bash
shield-agent logs                              # 최근 50개
shield-agent logs --last 100                   # 최근 100개
shield-agent logs --agent <id> --since 1h      # 특정 에이전트, 최근 1시간
shield-agent logs --method tools/call           # 특정 메서드
shield-agent logs --format json                # JSON 출력
```

---

## Web UI

`http://localhost:9090/ui` 로 접속.

- **초기 비밀번호**: `admin` (첫 로그인 시 변경 강제)
- **대시보드**: 실시간 요청 수, 에러율, 평균 레이턴시
- **로그**: 필터링 가능한 감사 로그 테이블
- **토큰 관리**: 토큰 발급/폐기/사용량 통계
- **미들웨어**: on/off 토글 (재시작해도 유지)
- **Agent Keys**: 공개키 등록/삭제 (keys.yaml 없이 Web UI로 관리)
- **Upstreams**: Gateway 모드 upstream 등록/수정/삭제

---

## Agent 평판 시스템

shield-agent는 에이전트의 행동을 추적하고 신뢰 점수를 계산하여 rate limit을 동적으로 조정합니다.

### 동작 원리

```
Action Logs → Score Calculator → Trust Level → Dynamic Rate Limit
  (SQLite)    (every 5 min)     trusted/      (2x, 1x, 0.25x, 0x)
                                 normal/
                                 suspicious/
                                 blocked
```

### 평판 시스템 활성화

```yaml
# shield-agent.yaml
reputation:
  enabled: true
  recalc_interval: 300    # recalculate every 5 minutes
  window_hours: 24        # look at last 24 hours of activity
  thresholds:
    trusted: 0.8
    normal: 0.4
    suspicious: 0.1
  rate_multipliers:
    trusted: 2.0          # 2x base rate limit
    normal: 1.0           # base rate
    suspicious: 0.25      # 1/4 base rate
    blocked: 0.0          # reject all requests
```

### CLI

```bash
# List all agent reputations
shield-agent reputation

# Query a specific agent
shield-agent reputation <agent-hash>

# JSON output
shield-agent reputation --format json
```

### 평판 API

평판 시스템이 활성화되면 모니터 서버(`:9090`)에서 다음 엔드포인트를 사용할 수 있습니다:

| Method | Path | 설명 |
|--------|------|------|
| `GET` | `/api/reputation` | 전체 에이전트 점수 목록 |
| `GET` | `/api/reputation/{hash}` | 특정 에이전트 점수 조회 |
| `GET` | `/api/reputation/stats` | 집계 통계 |
| `POST` | `/api/reputation/report` | 원격 인스턴스의 점수 수신 |
| `POST` | `/api/reputation/recalculate` | 즉시 재계산 트리거 |

### 신뢰 점수 요소

| 요소 | 가중치 | 설명 |
|------|--------|------|
| 성공률 | +0.35 | 성공한 요청의 비율 |
| 에러 페널티 | -0.25 | 실패한 요청의 비율 |
| 인증 실패 | -0.15 | 서명 검증 실패 횟수 |
| 볼륨 보너스 | +0.10 | 높은 트래픽 = 더 많은 신뢰 데이터 |
| 레이턴시 | -0.10 | 느린 응답은 신뢰도를 낮춤 |
| Rate limit 초과 | -0.05 | rate limit을 초과한 횟수 |

---

## 로드맵

자세한 내용은 [ROADMAP.md](ROADMAP.md)를 참고하세요.

| Phase | 상태 | 설명 |
|-------|------|------|
| Phase 1 — Core MVP | **완료** | Transport, Auth, Guard, Log, Middleware Chain |
| Phase 2 — 배포 & 설치 | **완료** | Docker, Homebrew, GoReleaser, CI/CD |
| Phase 3 — 토큰 & Web UI | **완료** | 토큰 관리, Web UI 대시보드 |
| Phase 3.5 — Gateway & DID | **완료** | Multi-upstream 라우팅, DID blocklist, verified 모드 |
| Phase 4 — 고도화 | **일부 완료** | Agent 평판 ✅, 프로토콜 자동 감지 ✅, WebSocket (예정) |

---

## 라이선스

[MIT](LICENSE)
