# shield-agent 테스트 가이드

실제로 따라하면서 shield-agent의 주요 기능을 테스트할 수 있는 실용 가이드입니다.

---

## 사전 준비

- **Go 1.22+** 설치
- `curl`, `jq` 설치된 터미널
- (선택) 프록시 모드 테스트용 MCP 서버 (예: [fastmcp](https://github.com/jlowin/fastmcp))

### 소스에서 빌드

```bash
git clone https://github.com/itdar/shield-agent.git
cd shield-agent
go build -o shield-agent ./cmd/shield-agent
```

바이너리 확인:

```bash
./shield-agent --help
```

### 기본 설정 파일 생성

```bash
cp shield-agent.example.yaml shield-agent.yaml
```

---

## 1. stdio 모드 — MCP 서버 래핑

stdio 모드에서 shield-agent는 자식 프로세스를 래핑하고 stdin/stdout의 JSON-RPC 메시지를 가로챕니다.

### 기본 테스트 (`echo` 사용)

```bash
echo '{"jsonrpc":"2.0","method":"initialize","id":1}' | ./shield-agent cat
```

`cat`은 stdin을 그대로 stdout으로 출력하므로, shield-agent가 양방향으로 JSON-RPC 메시지를 가로챕니다.

### 실제 MCP 서버 래핑

```bash
./shield-agent python server.py
./shield-agent --verbose node server.js --port 8080
```

`--verbose` 플래그는 로그 레벨을 `debug`로 설정하여 가로챈 모든 메시지를 확인할 수 있습니다.

### 모니터링 서버 확인

shield-agent가 stdio 모드로 실행 중일 때, 기본 주소에서 모니터링 서버가 시작됩니다:

```bash
curl -s http://127.0.0.1:9090/healthz | jq .
```

예상 출력:

```json
{
  "child_pid": 12345,
  "status": "healthy"
}
```

---

## 2. Proxy 모드 — 업스트림 MCP 서버 프록시

프록시 모드는 MCP 클라이언트와 업스트림 HTTP 기반 MCP 서버 사이에 위치합니다.

### 업스트림 MCP 서버 시작

fastmcp 서버가 있는 경우:

```bash
# 별도 터미널에서
python -m fastmcp run server.py --port 8000
```

### SSE 전송 방식

```bash
./shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport sse
```

SSE 엔드포인트 테스트:

```bash
# SSE 스트림 열기 (이벤트가 출력되면서 블로킹됨)
curl -N http://localhost:8888/sse
```

다른 터미널에서 메시지 전송 (SSE 스트림에서 받은 `<sessionId>` 값으로 교체):

```bash
curl -X POST "http://localhost:8888/messages?sessionId=<sessionId>" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"initialize","id":1}'
```

### Streamable HTTP 전송 방식

```bash
./shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport streamable-http
```

POST 요청 테스트:

```bash
curl -X POST http://localhost:8888/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"initialize","id":1}'
```

---

## 3. 로그 조회 CLI

shield-agent를 실행하고 메시지를 보낸 후, 저장된 로그를 조회합니다.

### 최근 10개 로그 항목 조회

```bash
./shield-agent logs --last 10
```

### 최근 1시간 로그를 JSON 형식으로

```bash
./shield-agent logs --since 1h --format json
```

### 에이전트 ID와 메서드로 필터링

```bash
./shield-agent logs --agent <id> --method tools/call
```

### 필터 조합

```bash
./shield-agent logs --last 5 --since 30m --method initialize --format json
```

기본 출력은 테이블 형식입니다:

```
TIMESTAMP          DIRECTION  METHOD                         OK    LATENCY_MS IP               AUTH
2026-03-25T10:...  in         initialize                     true  0.0                         unsigned
2026-03-25T10:...  out        initialize                     true  12.3                        unsigned
```

---

## 4. Rate Limit 검증

guard 미들웨어는 메서드별 속도 제한을 시행합니다. 낮은 제한값을 설정하여 테스트합니다.

### 1단계: 낮은 rate limit 설정

`shield-agent.yaml` 수정:

```yaml
middlewares:
  - name: auth
    enabled: true
  - name: guard
    enabled: true
    config:
      rate_limit_per_min: 3
  - name: log
    enabled: true
```

### 2단계: 프록시 시작

```bash
./shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport streamable-http
```

### 3단계: 빠른 연속 요청 전송

```bash
for i in $(seq 1 5); do
  echo "--- 요청 $i ---"
  curl -s -X POST http://localhost:8888/mcp \
    -H "Content-Type: application/json" \
    -d "{\"jsonrpc\":\"2.0\",\"method\":\"tools/list\",\"id\":$i}"
  echo
done
```

처음 3개 요청은 성공합니다. 4번째와 5번째 요청은 다음과 같은 JSON-RPC 에러 응답을 반환합니다:

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "error": {
    "code": -32600,
    "message": "rate limit exceeded for \"tools/list\""
  }
}
```

### 4단계: Prometheus 카운터 확인

```bash
curl -s http://127.0.0.1:9090/metrics | grep shield_agent_rate_limit_rejected_total
```

거부된 요청 수와 일치하는 카운터 값을 확인할 수 있습니다.

---

## 5. SIGHUP 설정 리로드

shield-agent는 `SIGHUP` 신호를 받으면 재시작 없이 설정 파일, 키 스토어, 미들웨어 체인을 핫 리로드합니다.

### 1단계: verbose 로깅으로 프록시 시작

```bash
./shield-agent --verbose proxy --listen :8888 --upstream http://localhost:8000
```

로그 출력에서 PID를 확인하거나 다음으로 찾습니다:

```bash
pgrep -f "shield-agent proxy"
```

### 2단계: 실행 중에 설정 파일 수정

예를 들어, `shield-agent.yaml`에서 보안 모드를 `open`에서 `closed`로 변경:

```yaml
security:
  mode: "closed"
```

또는 로그 레벨 변경:

```yaml
logging:
  level: "debug"
```

또는 미들웨어 활성화/비활성화:

```yaml
middlewares:
  - name: guard
    enabled: false
```

### 3단계: SIGHUP 전송

```bash
kill -SIGHUP $(pgrep -f "shield-agent proxy")
```

### 4단계: 로그 확인

리로드를 확인하는 로그 출력이 나타납니다:

```
{"level":"info","msg":"received SIGHUP, reloading configuration"}
{"level":"info","msg":"configuration reloaded successfully"}
```

이후 모든 요청에 새 설정이 즉시 적용됩니다.

---

## 6. TLS 모드

프록시는 인증서와 키를 제공하면 HTTPS를 지원합니다.

### 자체 서명 인증서 생성 (테스트용)

```bash
openssl req -x509 -newkey rsa:2048 -keyout key.pem -out cert.pem \
  -days 365 -nodes -subj "/CN=localhost"
```

### TLS로 프록시 시작

```bash
./shield-agent proxy \
  --listen :8888 \
  --upstream http://localhost:8000 \
  --transport streamable-http \
  --tls-cert cert.pem \
  --tls-key key.pem
```

`"msg":"TLS enabled","cert":"cert.pem"` 로그 메시지가 나타납니다.

### curl로 테스트

```bash
curl -k -X POST https://localhost:8888/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"initialize","id":1}'
```

`-k` 플래그는 자체 서명 인증서를 허용합니다. 프로덕션에서는 CA에서 발급한 인증서를 사용하세요.

### 설정 파일로 TLS 설정

CLI 플래그 대신 `shield-agent.yaml`에서 TLS를 설정할 수 있습니다:

```yaml
server:
  tls_cert: "/path/to/cert.pem"
  tls_key: "/path/to/key.pem"
```

CLI 플래그(`--tls-cert`, `--tls-key`)는 설정 파일 값보다 우선합니다.

---

## 7. 모니터링 엔드포인트

모니터링 서버는 기본적으로 `127.0.0.1:9090`에서 실행됩니다 (`--monitor-addr` 또는 설정 파일의 `server.monitor_addr`로 변경 가능).

### 루트 엔드포인트 — 서비스 인덱스

```bash
curl -s http://127.0.0.1:9090/ | jq .
```

예상 출력:

```json
{
  "endpoints": ["/healthz", "/metrics"],
  "service": "shield-agent"
}
```

### 헬스 체크

```bash
curl -s http://127.0.0.1:9090/healthz | jq .
```

stdio 모드 (자식 프로세스 실행 중):

```json
{
  "child_pid": 54321,
  "status": "healthy"
}
```

프록시 모드에서는 업스트림 서버도 프로브합니다. 업스트림이 다운된 경우:

```json
{
  "child_pid": 0,
  "status": "degraded"
}
```

### Prometheus 메트릭

```bash
curl -s http://127.0.0.1:9090/metrics
```

주요 메트릭:

| 메트릭 | 타입 | 설명 |
|--------|------|------|
| `shield_agent_messages_total` | Counter | 전체 JSON-RPC 메시지 수 (레이블: `direction`, `method`) |
| `shield_agent_auth_total` | Counter | 인증 이벤트 (레이블: `status`) |
| `shield_agent_message_latency_seconds` | Histogram | 메서드별 처리 레이턴시 |
| `shield_agent_child_process_up` | Gauge | 자식 프로세스 활성 여부 (stdio 모드, 1=alive) |
| `shield_agent_rate_limit_rejected_total` | Counter | 속도 제한된 요청 수 (레이블: `method`) |

shield-agent 메트릭만 필터링:

```bash
curl -s http://127.0.0.1:9090/metrics | grep "^shield_agent_"
```

### 커스텀 모니터 주소

```bash
./shield-agent --monitor-addr 0.0.0.0:9191 proxy --listen :8888 --upstream http://localhost:8000
```

커스텀 포트로 쿼리:

```bash
curl -s http://localhost:9191/healthz | jq .
```

---

## 8. 토큰 관리

Phase 3에서 추가된 토큰 기반 접근 제어를 테스트합니다.

### 토큰 생성

```bash
./shield-agent token create --name "test-agent" --quota-hourly 100 --quota-monthly 10000
```

출력 예시:
```
Token created successfully!
  ID:    a1b2c3d4e5f6g7h8
  Token: 4f8a9c2e...  (이 값을 지금 저장하세요. 다시 표시되지 않습니다)
```

### 토큰 목록 조회

```bash
./shield-agent token list
./shield-agent token list --all  # 비활성 토큰 포함
```

### 토큰으로 요청 보내기

프록시 모드에서 토큰 미들웨어가 활성화된 경우:

```bash
# Authorization: Bearer 헤더 사용
curl -X POST http://localhost:8888/mcp \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <토큰값>" \
  -d '{"jsonrpc":"2.0","method":"tools/list","id":1}'

# 또는 X-Shield-Token 헤더 사용
curl -X POST http://localhost:8888/mcp \
  -H "Content-Type: application/json" \
  -H "X-Shield-Token: <토큰값>" \
  -d '{"jsonrpc":"2.0","method":"tools/list","id":1}'
```

### 토큰 사용량 확인

```bash
./shield-agent token stats <토큰ID> --since 24h
```

### 토큰 폐기

```bash
./shield-agent token revoke <토큰ID>
```

### 토큰 미들웨어 활성화

`shield-agent.yaml`에 토큰 미들웨어를 추가합니다:

```yaml
middlewares:
  - name: auth
    enabled: true
  - name: guard
    enabled: true
  - name: token
    enabled: true
  - name: log
    enabled: true
```

---

## 9. Web UI — 관리 화면

shield-agent는 내장 웹 관리 UI를 제공합니다. 프록시 모드로 실행하면 모니터링 서버(기본 `:9090`)에서 접근 가능합니다.

### 접속

```
http://127.0.0.1:9090/ui
```

### 로그인

- 기본 비밀번호: `admin`
- 첫 로그인 시 비밀번호 변경이 요구됩니다

### 페이지 구성

| 페이지 | 기능 |
|--------|------|
| **Dashboard** | 최근 1시간 요청 수, 에러율, 평균 레이턴시, 활성 토큰 수 (10초 자동 갱신) |
| **Logs** | 최근 로그 테이블 (timestamp, direction, method, success, latency, IP, auth) |
| **Tokens** | 토큰 목록, 새 토큰 발급, 폐기 |
| **Settings** | 미들웨어 on/off 토글 |

### API 직접 테스트 (curl)

로그인 후 세션 쿠키를 사용합니다:

```bash
# 로그인
curl -c cookies.txt -X POST http://127.0.0.1:9090/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"password":"admin"}'

# 대시보드 데이터
curl -b cookies.txt http://127.0.0.1:9090/api/dashboard | jq .

# 로그 조회
curl -b cookies.txt "http://127.0.0.1:9090/api/logs?last=10" | jq .

# 토큰 목록
curl -b cookies.txt http://127.0.0.1:9090/api/tokens | jq .

# 미들웨어 상태
curl -b cookies.txt http://127.0.0.1:9090/api/middlewares | jq .
```

---

## 10. Guard 고급 기능

### Brute force 방어 테스트

`shield-agent.yaml`에서 brute force 방어를 활성화합니다:

```yaml
middlewares:
  - name: guard
    enabled: true
    config:
      brute_force_max_fails: 3
```

연속 실패 3회 후 해당 메서드가 10분간 자동 차단됩니다.

### JSON-RPC 검증 테스트

```yaml
middlewares:
  - name: guard
    enabled: true
    config:
      validate_jsonrpc: true
```

잘못된 JSON-RPC 요청을 보내면 거부됩니다:

```bash
# 빈 메서드 — 거부됨
curl -X POST http://localhost:8888/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"","id":1}'

# 잘못된 버전 — 거부됨
curl -X POST http://localhost:8888/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"1.0","method":"test","id":1}'
```

---

## 빠른 참조

| 시나리오 | 명령어 |
|----------|--------|
| stdio 래핑 | `./shield-agent <command> [args...]` |
| 프록시 (SSE) | `./shield-agent proxy --listen :8888 --upstream <url> --transport sse` |
| 프록시 (Streamable HTTP) | `./shield-agent proxy --listen :8888 --upstream <url>` |
| 프록시 (TLS) | `./shield-agent proxy --listen :8888 --upstream <url> --tls-cert cert.pem --tls-key key.pem` |
| 로그 조회 | `./shield-agent logs --last 10 --format json` |
| 토큰 생성 | `./shield-agent token create --name agent-1 --quota-hourly 100` |
| 토큰 목록 | `./shield-agent token list` |
| 토큰 폐기 | `./shield-agent token revoke <id>` |
| 토큰 통계 | `./shield-agent token stats <id> --since 24h` |
| Web UI | `http://127.0.0.1:9090/ui` (기본 비밀번호: admin) |
| 헬스 체크 | `curl http://127.0.0.1:9090/healthz` |
| Prometheus 메트릭 | `curl http://127.0.0.1:9090/metrics` |
| 설정 리로드 | `kill -SIGHUP $(pgrep -f "shield-agent")` |
| 미들웨어 비활성화 | `./shield-agent --disable-middleware guard proxy --upstream <url>` |
