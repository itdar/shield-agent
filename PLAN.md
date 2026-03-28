# shield-agent Improvement Plan

> QUESTIONS.md ~ QUESTIONS_4.md 문답에서 도출된 수정/개선 항목.
> ROADMAP.md Phase 1~3 완료 기준, Phase 4+ 및 신규 요구사항 반영.

---

## ~~Phase A — Release & 배포~~ (생략, 완성 후 진행)

---

## Phase B — Web UI 미들웨어 상태 영속화 (소규모, 빠른 개선)

> 출처: Q2-5 "미들웨어 toggle이 재시작하면 원복됨"

- [ ]  B-1. `admin_config` 테이블에 미들웨어 on/off 상태 저장 로직 추가
  - `internal/webui/api.go` — `handleMiddlewareToggle()`에서 DB 저장 추가
  - key 형식: `middleware_enabled_{name}` → value: `"true"` / `"false"`
- [ ]  B-2. 서버 시작 시 `admin_config`에서 미들웨어 상태 로드 → YAML 기본값 위에 덮어쓰기
  - `cmd/shield-agent/proxy.go`, `cmd/shield-agent/stdio.go` 초기화 로직에 추가
- [ ]  B-3. 테스트: Web UI에서 미들웨어 toggle → 재시작 → 상태 유지 확인

---

## Phase C — 공개키 Web UI 관리 (중규모)

> 출처: Q2-8 "keys.yaml 수동 편집 → Web UI에서 등록/삭제"

- [ ]  C-1. SQLite migration v6: `agent_keys` 테이블 생성

  ```sql
  CREATE TABLE agent_keys (
      id         TEXT PRIMARY KEY,   -- agent ID
      public_key TEXT NOT NULL,      -- base64 Ed25519 공개키
      label      TEXT,               -- 관리용 라벨
      created_at DATETIME NOT NULL,
      active     BOOLEAN DEFAULT 1
  );
  ```

  - `internal/storage/db.go`에 migration 추가
- [ ]  C-2. `DBKeyStore` 구현 — `auth.KeyStore` 인터페이스 구현, SQLite에서 조회

  - `internal/auth/db_store.go` (신규)
- [ ]  C-3. `CompositeKeyStore` — FileKeyStore + DBKeyStore 결합 (파일 우선, DB fallback 또는 합집합)

  - `internal/auth/composite_store.go` (신규)
- [ ]  C-4. Web UI API 추가 (`internal/webui/api.go`)

  - `GET /api/keys` — 등록된 공개키 목록
  - `POST /api/keys` — 공개키 등록 (agent ID + base64 공개키)
  - `DELETE /api/keys/{id}` — 공개키 삭제
- [ ]  C-5. proxy/stdio 초기화에서 CompositeKeyStore 사용하도록 변경

  - `cmd/shield-agent/proxy.go`, `cmd/shield-agent/stdio.go`
- [ ]  C-6. 테스트: Web UI에서 키 등록 → 해당 agent로 서명 요청 → 인증 성공 확인

---

## Phase D — Multi-Upstream Gateway 모드 (대규모, 핵심 신규 기능)

> 출처: Q1-4, Q1-5, Q2-2, Q2-3, Q3-2

### D-1. Config 확장

- [ ]  D-1-1. `UpstreamConfig` 구조체 정의 (`internal/config/config.go`)
  ```go
  type UpstreamMatch struct {
      Host        string `yaml:"host"`
      PathPrefix  string `yaml:"path_prefix"`
      StripPrefix bool   `yaml:"strip_prefix"`
  }
  type UpstreamConfig struct {
      Name          string        `yaml:"name"`
      URL           string        `yaml:"url"`
      Match         UpstreamMatch `yaml:"match"`
      Transport     string        `yaml:"transport"`
      TLSSkipVerify bool          `yaml:"tls_skip_verify"`
      TLSClientCert string        `yaml:"tls_client_cert"`
      TLSClientKey  string        `yaml:"tls_client_key"`
  }
  ```
- [ ]  D-1-2. `Config.Upstreams []UpstreamConfig` 필드 추가
- [ ]  D-1-3. `shield-agent.example.yaml`에 upstreams 섹션 예시 추가
- [ ]  D-1-4. `Validate()`에 upstreams 검증 로직 추가 (중복 name, 필수 URL 등)

### D-2. Router 구현

- [ ]  D-2-1. `internal/transport/proxy/router.go` (신규 파일)
  - `Router` 구조체: routes 배열
  - `Match(req *http.Request) *UpstreamConfig` — Host+Path > Host > Path > nil
  - `Handler()` — http.Handler 반환, 매칭된 upstream으로 dispatch
- [ ]  D-2-2. Router 단위 테스트 (`router_test.go`)
  - Host 매칭, Path 매칭, Host+Path 매칭, strip_prefix, 매칭 실패(404)

### D-3. Proxy 핸들러 변경

- [ ]  D-3-1. `proxy.go` RunE 로직 변경
  - `--upstream` 플래그: 선택적으로 변경 (upstreams 설정 있으면 불필요)
  - upstreams 설정이 있으면 Router 생성, 없으면 기존 단일 upstream 동작 유지 (하위 호환)
- [ ]  D-3-2. SSE/Streamable proxy가 동적 upstream을 받을 수 있도록 수정
  - `internal/transport/proxy/sse.go`, `streamable.go`

### D-4. Storage & Metrics에 upstream 추적

- [ ]  D-4-1. SQLite migration v7: `action_logs`에 `upstream_name` 컬럼 추가
- [ ]  D-4-2. `ActionLog` 구조체에 `UpstreamName` 필드 추가 (`internal/storage/db.go`)
- [ ]  D-4-3. Prometheus 메트릭에 `upstream` 라벨 추가 (`internal/monitor/server.go`)
- [ ]  D-4-4. `shield-agent logs` CLI에 `--upstream` 필터 추가

### D-5. Web UI에서 upstream 동적 관리

- [ ]  D-5-1. SQLite migration v8: `upstreams` 테이블 생성
  ```sql
  CREATE TABLE upstreams (
      name           TEXT PRIMARY KEY,
      url            TEXT NOT NULL,
      match_host     TEXT DEFAULT '',
      match_prefix   TEXT DEFAULT '',
      strip_prefix   BOOLEAN DEFAULT 0,
      transport      TEXT DEFAULT 'streamable-http',
      tls_skip_verify BOOLEAN DEFAULT 0,
      active         BOOLEAN DEFAULT 1,
      created_at     DATETIME NOT NULL
  );
  ```
- [ ]  D-5-2. Web UI API (`internal/webui/api.go`)
  - `GET /api/upstreams` — 목록 조회
  - `POST /api/upstreams` — 등록
  - `PUT /api/upstreams/{name}` — 수정
  - `DELETE /api/upstreams/{name}` — 삭제
- [ ]  D-5-3. Router hot-reload: upstream 변경 시 router 재구성 (SIGHUP 또는 즉시)
- [ ]  D-5-4. 프론트엔드 UI: upstream 관리 페이지

### D-6. upstream별 TLS 설정 (선택적)

> 출처: Q1-6, Q2-1
>
> **nginx 앞에 두는 사용자**: Phase D 전체가 무관하게 동작함.
>
> - 인바운드 TLS (`--tls-cert/--tls-key`): 이미 선택적. nginx가 TLS termination하면 shield-agent는 HTTP로 받으면 됨
> - 아웃바운드 TLS (upstream별): upstream URL이 `http://`면 TLS 설정 불필요. `https://` upstream일 때만 관련
> - 즉, nginx → shield-agent (HTTP) → upstream (HTTP) 구성이면 **TLS 설정 전혀 불필요**
> - D-6 항목은 upstream이 HTTPS인 경우만을 위한 선택적 기능

- [ ]  D-6-1. upstream별 `tls_skip_verify`, client cert/key 지원
  - reverse proxy에 upstream별 `http.Transport` 생성
  - 모든 TLS 설정은 optional — 미설정 시 Go 기본 TLS 동작 (시스템 CA 사용)
- [ ]  D-6-2. 테스트: self-signed cert upstream 연결, mTLS upstream 연결
- [ ]  D-6-3. 문서에 배포 패턴 가이드 추가
  - 패턴 1: shield-agent 단독 (인바운드 TLS 직접)
  - 패턴 2: nginx/caddy → shield-agent (TLS termination 위임)
  - 패턴 3: 내부망 HTTP only

### D-7. 통합 테스트

- [ ]  D-7-1. multi-upstream e2e 테스트 (Host 기반 + Path 기반 라우팅)
- [ ]  D-7-2. 기존 단일 upstream 모드 regression 테스트 (하위 호환성)

---

## Phase E — DID 에이전트 제어 강화 (중규모)

> 출처: Q3-4 "DID는 아무나 접근 가능 → 정책 필요"
>
> **설계 원칙**: DID의 핵심 장점은 "사전 등록 불필요". allowlist에 DID를 등록하게 만들면
> keys.yaml과 다를 바 없어서 DID를 쓰는 의미가 없어짐.
> → allowlist/blocklist 대신 **행동 기반 제어**로 접근.

- [ ]  E-1. DID 에이전트 기본 rate limit 차등 적용
  - 현재: guard middleware의 rate limit은 method별 전역 적용
  - 변경: agent ID 기반 per-agent rate limit 지원
  - DID 에이전트(미등록): 보수적 기본 rate limit (예: 10/분)
  - keys.yaml/DB 등록 에이전트: 관대한 rate limit (예: 100/분)
  - Token 에이전트: 토큰별 쿼터 적용 (현재와 동일)
  - `internal/middleware/guard.go` 수정
- [ ]  E-2. DID 에이전트에도 쿼터 지원 (현재 Token에만 있음)
  - guard middleware에서 agent ID(sha256 해시) 기반 사용량 추적
  - SQLite에 per-agent 사용량 기록 (action_logs에서 집계 또는 별도 테이블)
- [ ]  E-3. DID blocklist (최소한의 차단 기능)
  - **allowlist는 만들지 않음** (DID 장점 훼손)
  - blocklist만 지원: 악의적으로 확인된 DID만 차단
  - `security.did_blocklist: ["did:key:z6Mk...악성DID"]`
  - guard middleware에서 체크, SIGHUP으로 동적 리로드
- [ ]  E-4. `security.mode` 확장
  - 현재: `open` (경고만) / `closed` (거부)
  - 추가: `verified` — 유효한 서명이면 통과하되, 미등록 DID는 낮은 rate limit 적용
  - `open` → 모두 통과 (서명 없어도)
  - `verified` → 유효한 서명 필수, 미등록 DID는 제한적 접근
  - `closed` → 등록된 에이전트(keys.yaml/DB)만 접근
- [ ]  E-5. 테스트: DID 에이전트 rate limit 차등 적용, blocklist 차단 확인

---

## Phase F — 사이드카 중앙 로그 수집 (중규모)

> 출처: Q4-1 "사이드카 모드에서 감사 로그 중앙 수집 불가"

- [ ]  F-1. Telemetry collector 활성화 및 문서화
  - `internal/telemetry/collector.go` 이미 있음 (batch 전송, 차등 프라이버시)
  - 사용법 문서 작성: 사이드카 → 중앙 텔레메트리 엔드포인트 구성 가이드
- [ ]  F-2. 텔레메트리 수신 서버 (선택적)
  - shield-agent에 텔레메트리 수신 모드 추가? 또는 별도 서비스?
  - 수신한 이벤트를 중앙 SQLite/Postgres에 저장
- [ ]  F-3. Prometheus multi-target scrape 가이드 문서
  - `prometheus.yml` 예시 + Grafana 대시보드 JSON export 제공

---

## Phase G — 문서 (진행 가능)

> 출처: ROADMAP Phase 2.2 미완료 항목

- [ ]  G-1. README.md 영문 버전 (오픈소스 메인용)
- [ ]  G-2. CONTRIBUTING.md
- [ ]  G-3. CODE_OF_CONDUCT.md
- [ ]  G-4. 인증 가이드 문서 (Ed25519 vs DID vs Token 사용 시나리오별 안내)
  - 키 생성 → 등록 → 요청 예시 (Go, Python, Node.js)
- [ ]  G-5. Gateway 모드 설정 가이드 (Phase D 완료 후)
- [ ]  G-6. Prometheus + Grafana 대시보드 구성 가이드

---

## Phase H — ROADMAP Phase 4 항목 (장기)

> 기존 ROADMAP에 있던 미완료 항목 + Q&A에서 보강된 내용

- [ ]  H-1. Agent 평판 시스템 (로컬 점수 + 외부 조회)
- [ ]  H-2. 프로토콜 자동 감지 (A2A / JSON-RPC / REST)
- [ ]  H-3. WebSocket MCP transport 지원
- [ ]  H-4. 민감 헤더 마스킹 (Authorization, Cookie)
- [ ]  H-5. A2A spec agent card 기반 신원 검증

---

## 실행 순서 요약

```
Phase B (MW 영속)  ← 소규모, 1일 이내, 즉시 착수 가능
  ↓
Phase C (키 관리)  ← 중규모, 2~3일
  ↓
Phase D (Gateway)  ← 대규모 핵심, 1~2주
  ↓                   ※ nginx 앞에 두는 사용자는 TLS 설정 무관
Phase E (DID 제어) ← D와 병렬 가능
  ↓                   ※ allowlist 없음, 행동 기반 제어 (rate limit 차등)
Phase F (중앙 로그) ← D 이후 또는 병렬
  ↓
Phase G (문서)     ← 기능 완성 후 병렬 진행
  ↓
Phase A (배포)     ← 생략, 전부 완성 후 진행 예정
  ↓
Phase H (장기)     ← 시장 반응 보고 결정
```
