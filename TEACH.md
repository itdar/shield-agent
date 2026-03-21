# rua 프로젝트 이해하기

Go를 거의 안 써본 분을 위한 설명서입니다.

---

## 1. 이 프로젝트가 뭐 하는 건가요?

`rua`는 **MCP(Model Context Protocol) 프록시**입니다.

AI 에이전트(Claude 등)가 MCP 서버를 실행할 때, 그 사이에 끼어들어서:
- 요청/응답을 **가로채고 (intercept)**
- **서명 검증 (Ed25519)** 을 하고
- **SQLite에 로그를 남기고**
- **ripe 서버로 텔레메트리를 전송**합니다

```
AI 에이전트
    ↓ stdin (JSON-RPC 요청)
[ rua / mcp-shield ]  ← 여기가 이 프로젝트
    ↓ stdin
MCP 서버 (python, node 등)
    ↓ stdout (JSON-RPC 응답)
[ rua / mcp-shield ]
    ↓ stdout
AI 에이전트
```

실행 방법:
```bash
mcp-shield python my_mcp_server.py
mcp-shield node server.js --port 8080
```

---

## 2. 프로젝트 구조

```
rua/
├── cmd/
│   └── mcp-shield/
│       └── main.go          # 진입점 (여기서 시작)
├── internal/
│   ├── auth/                # Ed25519 서명 검증
│   ├── config/              # YAML 설정 파일 로딩
│   ├── jsonrpc/             # JSON-RPC 메시지 파싱
│   ├── logging/             # slog 로거 설정
│   ├── middleware/          # 미들웨어 체인 (요청/응답 파이프라인)
│   ├── monitor/             # Prometheus 메트릭 + HTTP 모니터링
│   ├── process/             # 자식 프로세스 실행 + stdin/stdout 파이프
│   ├── storage/             # SQLite 로깅
│   └── telemetry/           # ripe로 이벤트 전송
├── go.mod                   # 의존성 목록 (package.json 같은 것)
└── mcp-shield.example.yaml  # 설정 파일 예시
```

**`internal/`** 폴더가 핵심입니다. Go에서 `internal` 패키지는 **이 모듈 내부에서만** 가져다 쓸 수 있습니다 — 외부 공개 API가 아니라는 표시입니다.

---

## 3. Go 기초 문법 — 이 코드에서 쓰인 것들

### 3-1. `struct` — 데이터 묶음

```go
type Event struct {
    AgentIDHash      string    `json:"agentIdHash"`
    Timestamp        time.Time `json:"timestamp"`
    Method           string    `json:"method"`
    Success          bool      `json:"success"`
}
```

- TypeScript의 `interface`나 Kotlin의 `data class`와 비슷합니다
- 백틱(`) 안의 내용은 **태그**로, JSON 직렬화 시 필드 이름을 지정합니다
- `omitempty`를 붙이면 값이 비어있을 때 JSON 출력에서 제외됩니다

### 3-2. `interface` — 행동 규약

```go
// storage 패키지가 정의한 인터페이스
type Recorder interface {
    Record(event telemetry.Event)
}

// telemetry.Collector가 Record()를 구현하므로 자동으로 Recorder를 만족
var _ Recorder = (*telemetry.Collector)(nil) // 컴파일 타임 체크 (관용구)
```

Go의 인터페이스는 **묵시적 구현**입니다. `implements Recorder`라고 명시하지 않아도, `Record()` 메서드만 있으면 자동으로 인터페이스를 만족합니다.

테스트에서 `mockRecorder`를 만든 것도 이 덕분입니다 — 실제 Collector 없이 인터페이스만 만족하는 가짜 객체를 쉽게 만들 수 있습니다.

### 3-3. 메서드 (리시버)

```go
// (c *Collector)가 리시버 — Java의 this, Kotlin의 this와 같은 개념
func (c *Collector) Record(event Event) {
    if !c.enabled {
        return
    }
    // ...
}
```

`*Collector`의 `*`는 포인터 리시버입니다. 포인터로 받으면 원본 struct를 수정할 수 있습니다.

### 3-4. `goroutine` — 가벼운 동시 실행

```go
go lm.writer()       // 백그라운드에서 실행
go telCol.Run(ctx)   // 이것도 백그라운드
```

`go` 키워드 하나로 새 goroutine이 시작됩니다. Java의 Thread보다 훨씬 가볍고 (메모리 ~2KB), 수만 개를 띄워도 됩니다.

### 3-5. `channel` — goroutine 간 통신

```go
writeCh chan ActionLog  // ActionLog를 주고받는 채널

// 보내기 (non-blocking, 꽉 차면 버림)
select {
case lm.writeCh <- log:
default:
    lm.logger.Warn("channel full, dropping")
}

// 받기 (channel이 닫힐 때까지 반복)
for log := range lm.writeCh {
    lm.db.Insert(log)
}
```

채널은 goroutine 사이의 안전한 데이터 통로입니다. `make(chan ActionLog, 512)`의 512는 버퍼 크기입니다.

### 3-6. `context` — 취소 전파

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

go telCol.Run(ctx)   // ctx가 취소되면 Run()도 종료

// Run() 내부
select {
case <-ticker.C:
    c.flush()
case <-ctx.Done():  // 취소 신호 수신
    c.flush()
    return
}
```

`context`는 취소 신호나 타임아웃을 goroutine 트리 전체에 전파하는 표준 방법입니다.

### 3-7. `defer` — 함수 종료 시 실행

```go
func runWrapper(...) error {
    db, _ := storage.Open(...)
    defer db.Close()     // 함수가 끝날 때 (에러로 끝나도) 반드시 실행

    logMW := storage.NewLogMiddleware(...)
    defer logMW.Close()  // 역순으로 실행됨
}
```

Java의 `try-finally`, Python의 `with`와 비슷합니다. 여러 개면 **역순**으로 실행됩니다.

### 3-8. 에러 처리

```go
db, err := storage.Open(cfg.Storage.DBPath)
if err != nil {
    return fmt.Errorf("opening database: %w", err)
}
```

Go는 예외(exception)가 없습니다. 에러를 값으로 반환하고 직접 확인합니다. `%w`는 에러를 래핑해서 나중에 `errors.As()`로 꺼낼 수 있게 합니다.

### 3-9. 임베딩 (Embedding)

```go
type LogMiddleware struct {
    middleware.PassthroughMiddleware  // 이름 없이 타입만 쓰면 임베딩
    db      *DB
    // ...
}
```

`PassthroughMiddleware`의 메서드들이 `LogMiddleware`에 자동으로 생깁니다. Java의 상속과 비슷하지만, 구성(composition)입니다.

---

## 4. 핵심 흐름 따라가기

### main.go → 실행 순서

```
1. config 로드 (YAML + 환경변수 + CLI 플래그)
2. SQLite DB 열기
3. Prometheus 메트릭 초기화
4. Ed25519 키스토어 로드
5. 미들웨어 체인 구성: AuthMiddleware → LogMiddleware
6. TelemetryCollector 시작 (goroutine)
7. 자식 프로세스 실행 (RunWithMiddleware)
   └── PipelineIn  goroutine: stdin  → 미들웨어 → 자식stdin
   └── PipelineOut goroutine: 자식stdout → 미들웨어 → stdout
8. 자식 종료 시 cleanup (DB닫기, telemetry flush)
```

### 요청 하나가 처리되는 과정

```
AI에이전트 → stdin (JSON-RPC request)
    ↓
PipelineIn (process/pipeline.go)
    ↓ JSON 파싱
Chain.ProcessRequest()
    ↓
AuthMiddleware.ProcessRequest()   → 서명 검증 ("verified"|"failed"|"unsigned")
    ↓ (차단 안 되면)
LogMiddleware.ProcessRequest()    → SQLite 저장 + telemetry.Record()
    ↓
자식 MCP 서버로 전달

자식 MCP 서버 → stdout (JSON-RPC response)
    ↓
PipelineOut
    ↓
Chain.ProcessResponse()
    ↓
LogMiddleware.ProcessResponse()   → latency 계산 + SQLite + telemetry.Record()
    ↓
AI에이전트로 전달
```

---

## 5. 실행 및 개발 방법

### 빌드

```bash
cd rua

# 빌드 (바이너리 생성)
go build ./cmd/mcp-shield/

# 실행
./mcp-shield python my_server.py

# 빌드 없이 바로 실행
go run ./cmd/mcp-shield/ python my_server.py
```

### 테스트

```bash
# 전체 테스트
go test ./...

# 특정 패키지만
go test ./internal/telemetry/...

# 상세 출력
go test -v ./internal/storage/...

# 특정 테스트 함수만
go test -run TestLogMiddlewareTelemetryResponse ./internal/storage/...

# 커버리지
go test -cover ./...
```

### 설정 파일

```yaml
# mcp-shield.yaml
telemetry:
  enabled: true
  endpoint: "http://localhost:8080"  # ripe 서버 주소
  batch_interval: 60                 # 초 단위

security:
  mode: "open"    # "closed"면 미서명 요청 차단

storage:
  db_path: "mcp-shield.db"
  retention_days: 30
```

환경변수로도 덮어쓸 수 있습니다:
```bash
MCP_SHIELD_TELEMETRY_ENABLED=true ./mcp-shield python server.py
```

### 로그 조회 (내장 CLI)

```bash
./mcp-shield logs                        # 최근 50개
./mcp-shield logs --last 100             # 최근 100개
./mcp-shield logs --method tools/call    # 필터링
./mcp-shield logs --since 1h             # 1시간 내
./mcp-shield logs --format json          # JSON 출력
```

---

## 6. 앞으로 추가할 만한 것들

### 난이도 낮음 ⭐

**authStatus를 LogMiddleware까지 제대로 연결하기**

현재 `SetAuthStatus()`가 있지만 `main.go`에서 호출이 안 됩니다. `AuthMiddleware`의 `onAuth` 콜백에 agentID와 reqID도 넘겨주면 됩니다.

```go
// auth/auth.go 콜백 시그니처 변경
onAuth func(reqID, agentHash, status string)
```

**설정에 salt 추가**

텔레메트리 collector의 salt가 지금 빈 문자열입니다. `config.yaml`에 `salt` 필드를 추가하고 주입하면 agentID 해싱이 더 안전해집니다.

---

### 난이도 중간 ⭐⭐

**rate limiting**

특정 에이전트가 너무 많은 요청을 보낼 때 차단하는 미들웨어. ripe에는 이미 `RateLimiterFilter`가 있는데, rua 쪽에도 추가하면 좋습니다.

```go
type RateLimitMiddleware struct {
    middleware.PassthroughMiddleware
    limiters map[string]*rate.Limiter  // golang.org/x/time/rate
    mu       sync.Mutex
}
```

**메서드별 허용/차단 규칙**

설정 파일에서 특정 JSON-RPC method를 허용/차단하는 규칙을 정의하는 미들웨어.

```yaml
security:
  rules:
    - method: "tools/call"
      allow: true
    - method: "resources/delete"
      allow: false
```

**모니터링 엔드포인트 확장**

현재 `/metrics` (Prometheus)만 있습니다. `/health`, `/status` 엔드포인트를 추가하면 k8s 등에서 헬스체크 가능합니다.

---

### 난이도 높음 ⭐⭐⭐

**실시간 이벤트 스트리밍**

모니터 서버에 SSE(Server-Sent Events) 엔드포인트를 추가해서, 웹 대시보드가 실시간으로 요청 흐름을 볼 수 있게 합니다.

**다중 키스토어 지원**

현재 파일 기반 키스토어만 있습니다. HTTP API 기반 키스토어 (원격 키 서버)를 `KeyStore` 인터페이스를 구현해서 추가할 수 있습니다.

**WebSocket 지원**

현재는 stdio(stdin/stdout) 기반 MCP만 지원합니다. WebSocket 기반 MCP 서버도 프록시할 수 있도록 확장할 수 있습니다.

---

## 7. 자주 쓰는 Go 패턴 모음

```go
// nil 체크 후 사용 (포인터나 인터페이스에 자주 씀)
if lm.recorder != nil {
    lm.recorder.Record(event)
}

// 에러 래핑
return fmt.Errorf("context: %w", err)

// 타입 단언 (인터페이스 → 구체 타입)
exitErr, ok := err.(*exec.ExitError)
if ok {
    code := exitErr.ExitCode()
}

// 짧은 변수 선언 (:= 은 선언+할당)
db, err := storage.Open(path)

// 빈 식별자 (반환값 무시)
n, _ := db.Purge(30)

// 가변 인자
func NewChain(items ...Middleware) *Chain { ... }
chain := middleware.NewChain(authMW, logMW)
```

---

## 8. 디버깅 팁

```bash
# 상세 로그로 실행
./mcp-shield --verbose python server.py

# 로그 레벨 지정
./mcp-shield --log-level debug python server.py

# 모니터링 서버 주소 변경
./mcp-shield --monitor-addr 0.0.0.0:9090 python server.py

# Prometheus 메트릭 확인
curl http://localhost:9090/metrics

# 테스트 실행 중 로그 보기
go test -v -run TestLogMiddleware ./internal/storage/...
```
