# Implementation Plan

> PLAN.md 기반 실제 코드 구현 체크박스. 파일별 변경 내용과 코드 스니펫 포함.

---

## Phase B — 미들웨어 상태 영속화

### B-1. DB에 미들웨어 상태 저장

- [ ] `internal/storage/db.go` — `SaveConfig()`, `LoadConfig()` 헬퍼 추가
  ```go
  func (db *DB) SaveConfig(key, value string) error {
      _, err := db.conn.Exec(
          "INSERT OR REPLACE INTO admin_config (key, value) VALUES (?, ?)", key, value)
      return err
  }

  func (db *DB) LoadConfig(key string) (string, error) {
      var val string
      err := db.conn.QueryRow(
          "SELECT value FROM admin_config WHERE key = ?", key).Scan(&val)
      return val, err
  }

  func (db *DB) LoadConfigPrefix(prefix string) (map[string]string, error) {
      rows, err := db.conn.Query(
          "SELECT key, value FROM admin_config WHERE key LIKE ?", prefix+"%")
      // ... iterate rows, return map
  }
  ```

### B-2. Web UI toggle에서 DB 저장

- [ ] `internal/webui/api.go` — `handleMiddlewareToggle()` 수정
  ```go
  // 기존 toggleMW 호출 뒤에 추가:
  if a.db != nil {
      key := fmt.Sprintf("middleware_enabled_%s", name)
      val := "false"
      if !currentEnabled {
          val = "true"
      }
      a.db.SaveConfig(key, val)
  }
  ```

### B-3. 서버 시작 시 DB에서 미들웨어 상태 로드

- [ ] `internal/config/config.go` — `ApplyDBOverrides()` 함수 추가
  ```go
  func ApplyDBOverrides(cfg *Config, overrides map[string]string) {
      for k, v := range overrides {
          if strings.HasPrefix(k, "middleware_enabled_") {
              name := strings.TrimPrefix(k, "middleware_enabled_")
              enabled := v == "true"
              SetMiddlewareEnabled(cfg, name, enabled)
          }
      }
  }
  ```

- [ ] `cmd/shield-agent/proxy.go` — `runProxy()` 내 middleware chain 빌드 전에 호출
  ```go
  // DB 열린 후, BuildChain 전에:
  if overrides, err := db.LoadConfigPrefix("middleware_enabled_"); err == nil {
      config.ApplyDBOverrides(&cfg, overrides)
  }
  ```

- [ ] `cmd/shield-agent/stdio.go` — 동일하게 적용

### B-4. 테스트

- [ ] `internal/storage/db_test.go` — `TestSaveLoadConfig` 추가
- [ ] `internal/webui/api_test.go` — toggle 후 DB 값 확인 테스트 추가

---

## Phase C — 공개키 Web UI 관리

### C-1. SQLite migration v6

- [ ] `internal/storage/db.go` — migrations 배열에 추가
  ```go
  {
      version: 6,
      sql: `
  CREATE TABLE IF NOT EXISTS agent_keys (
      id         TEXT PRIMARY KEY,
      public_key TEXT NOT NULL,
      label      TEXT DEFAULT '',
      created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
      active     BOOLEAN NOT NULL DEFAULT 1
  );
  CREATE INDEX IF NOT EXISTS idx_agent_keys_active ON agent_keys (active);`,
  },
  ```

### C-2. Storage 메서드 추가

- [ ] `internal/storage/db.go` — agent_keys CRUD 메서드
  ```go
  type AgentKey struct {
      ID        string    `json:"id"`
      PublicKey string    `json:"public_key"`
      Label     string    `json:"label"`
      CreatedAt time.Time `json:"created_at"`
      Active    bool      `json:"active"`
  }

  func (db *DB) InsertAgentKey(id, publicKey, label string) error
  func (db *DB) ListAgentKeys(includeInactive bool) ([]AgentKey, error)
  func (db *DB) DeleteAgentKey(id string) error
  func (db *DB) GetAgentKey(id string) (*AgentKey, error)
  ```

### C-3. DBKeyStore 구현

- [ ] `internal/auth/db_store.go` (신규 파일)
  ```go
  package auth

  import (
      "crypto/ed25519"
      "encoding/base64"
      "fmt"
      "github.com/itdar/shield-agent/internal/storage"
  )

  type DBKeyStore struct {
      db *storage.DB
  }

  func NewDBKeyStore(db *storage.DB) *DBKeyStore {
      return &DBKeyStore{db: db}
  }

  func (s *DBKeyStore) PublicKey(agentID string) (ed25519.PublicKey, error) {
      ak, err := s.db.GetAgentKey(agentID)
      if err != nil || ak == nil || !ak.Active {
          return nil, fmt.Errorf("agent %q not found in DB key store", agentID)
      }
      raw, err := base64.StdEncoding.DecodeString(ak.PublicKey)
      if err != nil || len(raw) != ed25519.PublicKeySize {
          return nil, fmt.Errorf("agent %q: invalid key in DB", agentID)
      }
      return ed25519.PublicKey(raw), nil
  }
  ```

### C-4. CompositeKeyStore 구현

- [ ] `internal/auth/composite_store.go` (신규 파일)
  ```go
  package auth

  import "crypto/ed25519"

  type CompositeKeyStore struct {
      stores []KeyStore
  }

  func NewCompositeKeyStore(stores ...KeyStore) *CompositeKeyStore {
      return &CompositeKeyStore{stores: stores}
  }

  func (c *CompositeKeyStore) PublicKey(agentID string) (ed25519.PublicKey, error) {
      var lastErr error
      for _, s := range c.stores {
          key, err := s.PublicKey(agentID)
          if err == nil {
              return key, nil
          }
          lastErr = err
      }
      return nil, lastErr
  }
  ```

### C-5. Web UI API

- [ ] `internal/webui/api.go` — 키 관리 API 추가
  ```go
  func (a *API) RegisterRoutes(mux *http.ServeMux) {
      // ... 기존 라우트
      mux.HandleFunc("/api/keys", a.requireAuth(a.handleKeys))
      mux.HandleFunc("/api/keys/", a.requireAuth(a.handleKeyByID))
  }

  func (a *API) handleKeys(w http.ResponseWriter, r *http.Request)     // GET: list, POST: create
  func (a *API) handleKeyByID(w http.ResponseWriter, r *http.Request)  // DELETE /api/keys/{id}
  ```
  - POST body: `{"id": "agent-1", "public_key": "base64...", "label": "My Agent"}`
  - 유효성 검증: base64 디코딩 → 32바이트 Ed25519 확인
  - 중복 ID 체크

### C-6. proxy/stdio 초기화 변경

- [ ] `cmd/shield-agent/proxy.go` — KeyStore 생성 부분 수정
  ```go
  // 현재:
  fileStore, _ := auth.NewFileKeyStore(cfg.Security.KeyStorePath)
  cachedStore := auth.NewCachedKeyStore(fileStore, 5*time.Minute)

  // 변경 후:
  fileStore, _ := auth.NewFileKeyStore(cfg.Security.KeyStorePath)
  dbStore := auth.NewDBKeyStore(db)
  composite := auth.NewCompositeKeyStore(fileStore, dbStore)
  cachedStore := auth.NewCachedKeyStore(composite, 5*time.Minute)
  ```

- [ ] `cmd/shield-agent/stdio.go` — 동일 패턴 적용 (DB 접근 가능한 경우)

### C-7. 테스트

- [ ] `internal/auth/db_store_test.go` — DBKeyStore 단위 테스트
- [ ] `internal/auth/composite_store_test.go` — CompositeKeyStore 단위 테스트 (파일 실패 → DB fallback)
- [ ] `internal/storage/db_test.go` — AgentKey CRUD 테스트
- [ ] `internal/webui/api_test.go` — /api/keys CRUD API 테스트

---

## Phase D — Multi-Upstream Gateway

### D-1. Config 확장

- [ ] `internal/config/config.go` — 구조체 추가
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
      Transport     string        `yaml:"transport"`       // "sse" | "streamable-http"
      TLSSkipVerify bool          `yaml:"tls_skip_verify"` // optional
      TLSClientCert string        `yaml:"tls_client_cert"` // optional, mTLS
      TLSClientKey  string        `yaml:"tls_client_key"`  // optional, mTLS
  }
  ```

- [ ] `internal/config/config.go` — Config에 필드 추가
  ```go
  type Config struct {
      // ... 기존 필드
      Upstreams []UpstreamConfig `yaml:"upstreams,omitempty"`
  }
  ```

- [ ] `internal/config/config.go` — `Validate()`에 검증 추가
  ```go
  // Upstreams 검증
  names := make(map[string]bool)
  for _, u := range cfg.Upstreams {
      if u.Name == "" { return fmt.Errorf("upstream name required") }
      if u.URL == "" { return fmt.Errorf("upstream %q: url required", u.Name) }
      if names[u.Name] { return fmt.Errorf("duplicate upstream name %q", u.Name) }
      names[u.Name] = true
      if u.Transport != "" && u.Transport != "sse" && u.Transport != "streamable-http" {
          return fmt.Errorf("upstream %q: invalid transport %q", u.Name, u.Transport)
      }
  }
  ```

- [ ] `shield-agent.example.yaml` — upstreams 예시 섹션 추가

- [ ] `internal/config/config_test.go` — upstreams 검증 테스트

### D-2. Router 구현

- [ ] `internal/transport/proxy/router.go` (신규 파일)
  ```go
  package proxy

  import (
      "net/http"
      "strings"
      "github.com/itdar/shield-agent/internal/config"
  )

  type Route struct {
      Upstream config.UpstreamConfig
      handler  http.Handler
  }

  type Router struct {
      routes       []Route
      defaultRoute *Route // fallback (단일 upstream 호환)
  }

  func NewRouter(upstreams []config.UpstreamConfig, handlerFactory func(config.UpstreamConfig) http.Handler) *Router

  func (r *Router) Match(req *http.Request) *Route {
      // 1. Host+Path 매칭 (가장 구체적)
      // 2. Host만 매칭
      // 3. Path만 매칭
      // 4. default fallback
      // 5. nil (404)
  }

  func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
      route := r.Match(req)
      if route == nil {
          http.Error(w, "no upstream matched", http.StatusBadGateway)
          return
      }
      route.handler.ServeHTTP(w, req)
  }
  ```

- [ ] `internal/transport/proxy/router_test.go` — 테스트
  - Host 매칭, Path 매칭, Host+Path 매칭
  - strip_prefix 동작
  - 매칭 실패 → 502
  - default route fallback

### D-3. Proxy 핸들러 변경

- [ ] `cmd/shield-agent/proxy.go` — `--upstream` 플래그를 선택적으로 변경
  ```go
  // 현재: _ = cmd.MarkFlagRequired("upstream")
  // 변경: required 제거, upstreams 설정과 --upstream 중 하나 필수
  ```

- [ ] `cmd/shield-agent/proxy.go` — `runProxy()` 핸들러 생성 로직 변경
  ```go
  var handler http.Handler

  if len(cfg.Upstreams) > 0 {
      // Gateway 모드: multi-upstream router
      handlerFactory := func(u config.UpstreamConfig) http.Handler {
          transport := u.Transport
          if transport == "" { transport = transportType }
          switch transport {
          case "sse":
              return proxy.NewSSEProxy(u.URL, swappableChain, logger, allowedOrigins).Handler()
          default:
              return proxy.NewStreamableProxy(u.URL, swappableChain, logger, allowedOrigins).Handler()
          }
      }
      handler = proxy.NewRouter(cfg.Upstreams, handlerFactory)
  } else if upstream != "" {
      // 기존 단일 upstream 모드 (하위 호환)
      switch transportType {
      case "sse":
          handler = proxy.NewSSEProxy(upstream, swappableChain, logger, allowedOrigins).Handler()
      default:
          handler = proxy.NewStreamableProxy(upstream, swappableChain, logger, allowedOrigins).Handler()
      }
  } else {
      return fmt.Errorf("either --upstream or upstreams config is required")
  }
  ```

### D-4. Storage & Metrics upstream 추적

- [ ] `internal/storage/db.go` — migration v7
  ```go
  {version: 7, sql: `ALTER TABLE action_logs ADD COLUMN upstream_name TEXT DEFAULT '';`},
  ```

- [ ] `internal/storage/db.go` — `ActionLog` 구조체에 필드 추가
  ```go
  type ActionLog struct {
      // ... 기존 필드
      UpstreamName string `json:"upstream_name"`
  }
  ```

- [ ] `internal/storage/db.go` — `Insert()`, `QueryLogs()` SQL 수정

- [ ] `internal/storage/db.go` — `QueryOptions`에 `UpstreamName` 필터 추가

- [ ] `internal/monitor/server.go` — 메트릭에 upstream 라벨 추가
  ```go
  // MessagesTotal에 "upstream" 라벨 추가
  MessagesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
      Name: "shield_agent_messages_total",
      Help: "Total number of JSON-RPC messages processed.",
  }, []string{"direction", "method", "upstream"}),
  ```

- [ ] `cmd/shield-agent/main.go` — `shield-agent logs --upstream <name>` 필터 추가

### D-5. Web UI upstream CRUD

- [ ] `internal/storage/db.go` — migration v8: upstreams 테이블
  ```go
  {version: 8, sql: `
  CREATE TABLE IF NOT EXISTS upstreams (
      name            TEXT PRIMARY KEY,
      url             TEXT NOT NULL,
      match_host      TEXT DEFAULT '',
      match_prefix    TEXT DEFAULT '',
      strip_prefix    BOOLEAN DEFAULT 0,
      transport       TEXT DEFAULT 'streamable-http',
      tls_skip_verify BOOLEAN DEFAULT 0,
      active          BOOLEAN DEFAULT 1,
      created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
  );`},
  ```

- [ ] `internal/storage/db.go` — Upstream CRUD 메서드
  ```go
  func (db *DB) ListUpstreams() ([]UpstreamRow, error)
  func (db *DB) InsertUpstream(row UpstreamRow) error
  func (db *DB) UpdateUpstream(name string, row UpstreamRow) error
  func (db *DB) DeleteUpstream(name string) error
  ```

- [ ] `internal/webui/api.go` — upstream API 엔드포인트
  ```go
  mux.HandleFunc("/api/upstreams", a.requireAuth(a.handleUpstreams))       // GET, POST
  mux.HandleFunc("/api/upstreams/", a.requireAuth(a.handleUpstreamByName)) // PUT, DELETE
  ```

- [ ] Router hot-reload: upstream DB 변경 시 router 재구성
  - 옵션 A: SIGHUP 트리거
  - 옵션 B: API 호출 시 즉시 router swap (SwappableRouter 패턴, SwappableChain과 유사)

### D-6. upstream별 TLS

- [ ] `internal/transport/proxy/transport.go` (신규 파일)
  ```go
  func BuildHTTPTransport(u config.UpstreamConfig) *http.Transport {
      tlsConfig := &tls.Config{
          InsecureSkipVerify: u.TLSSkipVerify,
      }
      if u.TLSClientCert != "" && u.TLSClientKey != "" {
          cert, _ := tls.LoadX509KeyPair(u.TLSClientCert, u.TLSClientKey)
          tlsConfig.Certificates = []tls.Certificate{cert}
      }
      return &http.Transport{TLSClientConfig: tlsConfig}
  }
  ```

- [ ] SSE/Streamable proxy에서 custom Transport 사용하도록 수정

### D-7. 테스트

- [ ] `internal/transport/proxy/router_test.go` — 라우팅 로직 단위 테스트
- [ ] `internal/storage/db_test.go` — upstream CRUD 테스트
- [ ] `internal/integration/` — multi-upstream e2e 테스트
- [ ] 기존 단일 upstream 모드 regression 테스트 (하위 호환성)

---

## Phase E — DID 에이전트 제어 강화

### E-1. per-agent rate limit

- [ ] `internal/middleware/guard.go` — `GuardConfig`에 per-agent 설정 추가
  ```go
  type GuardConfig struct {
      // ... 기존 필드
      AgentRateLimitDefault    int  // DID/미등록 에이전트 기본 (예: 10/분)
      AgentRateLimitRegistered int  // keys.yaml/DB 등록 에이전트 (예: 100/분)
  }
  ```

- [ ] `internal/middleware/guard.go` — `ProcessRequest()`에 agent ID 기반 rate limit 분기
  - context에서 agent ID 추출 (auth middleware가 먼저 실행되므로)
  - 등록 여부에 따라 다른 rate limit 적용
  - 기존 method별 rate limit과 별개로 per-agent limit 추가

- [ ] `internal/middleware/registry.go` — guard config에 새 필드 파싱 추가
  ```yaml
  # shield-agent.yaml
  middlewares:
    - name: guard
      config:
        rate_limit_per_min: 100
        agent_rate_limit_default: 10
        agent_rate_limit_registered: 100
  ```

### E-2. DID blocklist (allowlist 없음)

- [ ] `internal/config/config.go` — `SecurityConfig`에 필드 추가
  ```go
  type SecurityConfig struct {
      Mode         string   `yaml:"mode"`           // "open" | "verified" | "closed"
      KeyStorePath string   `yaml:"key_store_path"`
      DIDBlocklist []string `yaml:"did_blocklist"`   // 차단할 DID 목록
  }
  ```

- [ ] `internal/middleware/auth.go` — `ProcessRequest()`에 blocklist 체크 추가
  ```go
  if strings.HasPrefix(agentID, "did:key:") {
      for _, blocked := range a.didBlocklist {
          if agentID == blocked {
              record("blocked")
              return nil, fmt.Errorf("agent is blocked")
          }
      }
      pubKey, resolveErr = auth.ResolveDIDKey(agentID)
  }
  ```

- [ ] `internal/middleware/auth.go` — `AuthMiddleware` 구조체에 `didBlocklist []string` 추가
- [ ] `internal/middleware/registry.go` — auth 생성 시 blocklist 전달

### E-3. security.mode: verified 추가

- [ ] `internal/config/config.go` — `Validate()`에서 "verified" 허용
  ```go
  case "open", "verified", "closed":
  ```

- [ ] `internal/middleware/auth.go` — `ProcessRequest()` mode 분기 수정
  ```go
  // "open": 서명 없어도 통과
  // "verified": 서명 필수, 유효하면 통과 (미등록 DID도 OK, 단 rate limit 차등)
  // "closed": 등록된 에이전트만 통과
  if agentID == "" || sigHex == "" {
      record("unsigned")
      if a.mode == "verified" || a.mode == "closed" {
          return nil, fmt.Errorf("authentication required")
      }
      return req, nil // open 모드
  }
  ```

- [ ] context에 인증 결과 전달 (guard middleware에서 참조)
  ```go
  type authResult struct {
      AgentID    string
      Registered bool  // keys.yaml/DB에 등록된 에이전트인지
      DID        bool  // DID 방식인지
  }
  // context.WithValue(ctx, authResultKey{}, result)
  ```

### E-4. 테스트

- [ ] `internal/middleware/auth_test.go` — verified 모드 테스트 (unsigned 거부)
- [ ] `internal/middleware/auth_test.go` — DID blocklist 테스트
- [ ] `internal/middleware/guard_test.go` — per-agent rate limit 테스트 (등록 vs 미등록 차등)
- [ ] `internal/config/config_test.go` — verified 모드 validation

---

## Phase F — 사이드카 중앙 로그 수집

### F-1. Telemetry 문서화

- [ ] `docs/telemetry-guide.md` (신규)
  - 사이드카 → 중앙 텔레메트리 구성 방법
  - `shield-agent.yaml` telemetry 섹션 설명
  - 차등 프라이버시 epsilon 값 가이드

### F-2. Prometheus 가이드

- [ ] `docs/prometheus-guide.md` (신규)
  - multi-target `prometheus.yml` 예시
  - Grafana 대시보드 JSON export
  - 유용한 PromQL 쿼리 예시

---

## Phase G — 문서

- [ ] G-1. `README_EN.md` — 영문 README
- [ ] G-2. `CONTRIBUTING.md` — 기여 가이드
- [ ] G-3. `docs/auth-guide.md` — 인증 방식 가이드 (Ed25519, DID, Token 비교 + 코드 예시)
- [ ] G-4. `docs/gateway-guide.md` — Gateway 모드 설정 가이드 (Phase D 완료 후)
- [ ] G-5. `docs/deployment-patterns.md` — 배포 패턴 (사이드카, Gateway, nginx 연동)

---

## 실행 순서

```
B-1 → B-2 → B-3 → B-4           (1일, MW 영속화)
  ↓
C-1 → C-2 → C-3 → C-4 → C-5    (2일, DB KeyStore)
  → C-6 → C-7                    (1일, Web UI + 테스트)
  ↓
D-1 → D-2                        (2일, Config + Router)
  → D-3                           (2일, Proxy 변경)
  → D-4 → D-5                    (3일, Storage + Web UI)
  → D-6 → D-7                    (2일, TLS + 테스트)
  ↓
E-1 → E-2 → E-3 → E-4           (3일, DID 제어, D와 병렬 가능)
  ↓
F-1 → F-2                        (1일, 문서)
  ↓
G-1 ~ G-5                        (2일, 문서)
```
