# Case Test Guide

shield-agent 기능별 수동 테스트 방법.

---

## 사전 준비

```bash
# 빌드
go build -o shield-agent ./cmd/shield-agent

# 테스트용 MCP 서버 (간단한 echo)
# Python: pip install fastmcp
# 또는 아무 HTTP 서버로 대체 가능
python -m http.server 8000 &
```

---

## 1. stdio 모드 — MCP 서버 래핑

**목적:** 자식 프로세스 stdin/stdout 인터셉트, 미들웨어 적용

```bash
# 기본 실행
./shield-agent python -m http.server 8000

# verbose 모드
./shield-agent --verbose echo "hello"
```

**확인:** (직접 테스트 필요 — 인터랙티브 프로세스)
- [ ] 자식 프로세스 정상 실행
- [ ] `Ctrl+C` 시 자식 프로세스도 종료
- [ ] monitor :9090 접근 가능 (`curl localhost:9090/healthz`)

---

## 2. proxy 모드 — 단일 upstream

**목적:** HTTP 리버스 프록시 동작

```bash
# upstream 서버 실행
python -m http.server 8000 &

# shield-agent proxy
./shield-agent proxy --listen :8888 --upstream http://localhost:8000
```

**테스트:**
```bash
# Streamable HTTP
curl -X POST http://localhost:8888/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"tools/list","id":1}'

# 헬스 체크
curl http://localhost:9090/healthz
```

**확인:**
- [x] 요청이 upstream으로 전달됨
- [x] `/healthz` 정상 응답 (`{"child_pid":0,"status":"healthy"}`)
- [x] `/metrics` Prometheus 형식 출력

---

## 3. proxy 모드 — SSE 전송

```bash
./shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport sse
```

**테스트:**
```bash
# SSE 연결
curl -N http://localhost:8888/sse
```

**확인:** (SSE upstream 필요 — 직접 테스트 필요)
- [ ] SSE 스트림 연결됨
- [ ] `/messages` 엔드포인트로 메시지 전송 가능

---

## 4. Gateway 모드 — 멀티 upstream

**목적:** Host/Path 기반 라우팅

```bash
# upstream 서버 2개 (각각 다른 디렉토리)
mkdir -p /tmp/sa /tmp/sb
echo "server-a" > /tmp/sa/test.txt
echo "server-b" > /tmp/sb/test.txt
python -m http.server 8001 --directory /tmp/sa &
python -m http.server 8002 --directory /tmp/sb &
```

`shield-agent.yaml`:
```yaml
server:
  monitor_addr: "127.0.0.1:9090"
security:
  mode: open
middlewares:
  - name: auth
    enabled: true
  - name: guard
    enabled: true
  - name: log
    enabled: true
upstreams:
  - name: server-a
    url: http://localhost:8001
    match:
      path_prefix: /a
      strip_prefix: true
  - name: server-b
    url: http://localhost:8002
    match:
      path_prefix: /b
      strip_prefix: true
```

```bash
./shield-agent proxy --listen :8888
```

**테스트:**
```bash
curl http://localhost:8888/a/test.txt   # → "server-a"
curl http://localhost:8888/b/test.txt   # → "server-b"
curl http://localhost:8888/c/           # → 502
```

**확인:**
- [x] /a 요청 → 8001 도착 (응답: "server-a")
- [x] /b 요청 → 8002 도착 (응답: "server-b")
- [x] 매칭 안 되는 요청 → 502 응답

---

## 5. TLS (HTTPS)

```bash
# self-signed 인증서 생성 (macOS LibreSSL 호환을 위해 RSA 사용)
openssl req -x509 -newkey rsa:2048 -keyout key.pem -out cert.pem -days 1 -nodes -subj '/CN=localhost'

# HTTPS 프록시
./shield-agent proxy --listen :8888 --upstream http://localhost:8000 \
  --tls-cert cert.pem --tls-key key.pem
```

**테스트:**
```bash
curl -k https://localhost:8888/test.txt    # HTTPS 접속
curl http://localhost:8888/                 # HTTP → 에러
```

**확인:**
- [x] HTTPS 접속 성공 (upstream 응답 정상 수신)
- [x] HTTP 요청 → "Client sent an HTTP request to an HTTPS server" 에러

> **참고:** macOS LibreSSL은 ed25519 인증서를 지원하지 않을 수 있음. RSA 인증서 사용 권장.

---

## 6. 토큰 기반 인증

```bash
# 토큰 발급
./shield-agent token create --name "test-agent" --quota-hourly 10

# 토큰 목록
./shield-agent token list

# proxy 실행 (token 미들웨어 활성화)
./shield-agent proxy --listen :8888 --upstream http://localhost:8000 \
  --enable-middleware token
```

**테스트:**
```bash
TOKEN="발급받은토큰"

# 토큰 포함 요청
curl http://localhost:8888/mcp \
  -X POST -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"jsonrpc":"2.0","method":"tools/list","id":1}'

# 쿼터 초과 테스트 (11번 반복)
for i in $(seq 1 11); do
  curl -s http://localhost:8888/mcp \
    -X POST -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"jsonrpc\":\"2.0\",\"method\":\"test\",\"id\":$i}"
done

# 토큰 폐기
./shield-agent token revoke <token-id>
```

**확인:**
- [x] 토큰 발급 성공 (ID + Token 출력)
- [x] 토큰 목록 조회 성공 (테이블 출력)
- [x] 토큰 stats 조회 성공
- [x] 토큰 폐기 성공
- [ ] 유효한 토큰 → upstream 통과 (token MW 활성화 필요 — 직접 테스트)
- [ ] 쿼터 초과 → 거부 메시지 (직접 테스트)

---

## 7. Ed25519 서명 인증

```bash
# 키 생성 (Go로)
go run -exec '' <<'GOEOF'
package main
import (
    "crypto/ed25519"
    "crypto/rand"
    "encoding/base64"
    "fmt"
)
func main() {
    pub, priv, _ := ed25519.GenerateKey(rand.Reader)
    fmt.Println("public:", base64.StdEncoding.EncodeToString(pub))
    fmt.Println("private:", base64.StdEncoding.EncodeToString(priv))
}
GOEOF
```

`keys.yaml`:
```yaml
keys:
  - id: "test-agent"
    key: "위에서출력된public키"
```

```bash
./shield-agent proxy --listen :8888 --upstream http://localhost:8000
```

**확인:** (서명 생성 도구 필요 — 직접 테스트)
- [ ] `security.mode: open` — 서명 없어도 통과 (경고만)
- [ ] `security.mode: closed` — 서명 없으면 거부
- [ ] `security.mode: verified` — 서명 없으면 거부

---

## 8. Web UI 키 등록

```bash
./shield-agent proxy --listen :8888 --upstream http://localhost:8000
```

**테스트:**
```bash
# 로그인 (초기 비밀번호: admin)
curl -c cookies.txt http://localhost:9090/api/auth/login \
  -X POST -H "Content-Type: application/json" \
  -d '{"password":"admin"}'

# 키 등록
curl -b cookies.txt http://localhost:9090/api/keys \
  -X POST -H "Content-Type: application/json" \
  -d '{"id":"web-agent","public_key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=","label":"Test"}'

# 키 목록 확인
curl -b cookies.txt http://localhost:9090/api/keys

# 키 삭제
curl -b cookies.txt -X DELETE http://localhost:9090/api/keys/web-agent
```

**확인:**
- [x] 로그인 성공 (`{"force_change":true,"ok":true}`)
- [x] 키 등록 성공 (201, `{"id":"test-key-agent","ok":"key registered"}`)
- [x] 키 목록 조회 성공 (배열 반환)
- [x] 키 삭제 성공 (`{"ok":"key deleted"}`)
- [ ] 등록한 키로 서명 인증 가능 (서명 도구 필요 — 직접 테스트)

---

## 9. Web UI upstream 관리

```bash
# 로그인 후
curl -b cookies.txt http://localhost:9090/api/upstreams \
  -X POST -H "Content-Type: application/json" \
  -d '{"name":"dynamic-up","url":"http://localhost:8001","match_prefix":"/dyn"}'

# 목록
curl -b cookies.txt http://localhost:9090/api/upstreams

# 수정
curl -b cookies.txt -X PUT http://localhost:9090/api/upstreams/dynamic-up \
  -H "Content-Type: application/json" \
  -d '{"url":"http://localhost:8002"}'

# 삭제
curl -b cookies.txt -X DELETE http://localhost:9090/api/upstreams/dynamic-up
```

**확인:**
- [x] Create 성공 (`{"name":"dynamic-up","ok":"upstream created"}`)
- [x] List 성공 (배열 반환, 등록된 upstream 포함)
- [x] Update 성공 (`{"ok":"upstream updated"}`)
- [x] Delete 성공 (`{"ok":"upstream deleted"}`)

---

## 10. 미들웨어 토글 영속화

```bash
# 로그인 후 guard 미들웨어 비활성화
curl -b cookies.txt -X POST http://localhost:9090/api/middlewares/guard/toggle

# 상태 확인
curl -b cookies.txt http://localhost:9090/api/middlewares

# shield-agent 재시작
# Ctrl+C로 종료 후 다시 실행

# 재시작 후 상태 확인
curl -b cookies.txt http://localhost:9090/api/middlewares
```

**확인:**
- [x] 토글 후 상태 변경됨 (`guard: enabled: false`)
- [ ] 재시작 후에도 상태 유지됨 (인터랙티브 재시작 필요 — 직접 테스트)

> **참고:** DB에 `middleware_enabled_guard=false` 저장 확인됨. 재시작 시 `ApplyDBOverrides()`가 호출되어 로그에 "applied DB middleware overrides" 출력.

---

## 11. Guard — Rate Limit

`shield-agent.yaml`:
```yaml
middlewares:
  - name: guard
    enabled: true
    config:
      rate_limit_per_min: 3
```

```bash
# 4번 연속 요청 → 4번째에서 거부
for i in 1 2 3 4; do
  echo "--- request $i ---"
  curl -s http://localhost:8888/mcp \
    -X POST -H "Content-Type: application/json" \
    -d "{\"jsonrpc\":\"2.0\",\"method\":\"test\",\"id\":$i}"
done
```

**확인:**
- [x] 3번째까지 통과 (upstream 응답)
- [x] 4번째에서 rate limit 에러 (`"rate limit exceeded for method..."`)

---

## 12. Guard — IP 차단

```yaml
middlewares:
  - name: guard
    config:
      ip_blocklist:
        - "127.0.0.1/32"
```

**확인:** (직접 테스트 필요 — proxy 모드에서 IP 추출 방식 확인)
- [ ] localhost에서 요청 → 차단됨

---

## 13. DID blocklist

```yaml
security:
  mode: verified
  did_blocklist:
    - "did:key:z6MkBadAgent..."
```

**확인:** (DID 서명 도구 필요 — 직접 테스트)
- [ ] blocklist에 있는 DID → 차단
- [ ] blocklist에 없는 DID → 통과 (서명 유효 시)
- [ ] unsigned 요청 → 거부 (verified 모드)

---

## 14. 로그 조회

```bash
# 몇 가지 요청 보낸 후
./shield-agent logs
./shield-agent logs --last 5
./shield-agent logs --since 1h
./shield-agent logs --method tools/list
./shield-agent logs --format json
```

**확인:**
- [x] CLI 실행 정상 (테이블 헤더 출력)
- [ ] 실제 로그 데이터 표시 (비동기 쓰기 대기 후 확인 — 직접 테스트)

---

## 15. SIGHUP 핫 리로드

```bash
# shield-agent 실행 중
# shield-agent.yaml 수정 (예: rate_limit 변경)
kill -HUP $(pgrep shield-agent)
```

**확인:** (인터랙티브 — 직접 테스트)
- [ ] "configuration reloaded successfully" 로그 출력
- [ ] 변경된 설정 즉시 적용

---

## 16. Prometheus 메트릭

```bash
curl http://localhost:9090/metrics | grep shield_agent
```

**확인:**
- [x] `shield_agent_messages_total` 존재
- [x] `shield_agent_auth_total` 존재 (`{status="unsigned"} 1` 확인)
- [x] `shield_agent_message_latency_seconds` 존재
- [x] 요청 보낸 후 카운터 증가 확인

---

## 17. Web UI 대시보드

브라우저에서 `http://localhost:9090/ui` 접속

**확인:** (브라우저 필요 — 직접 테스트)
- [ ] 로그인 (admin / admin)
- [ ] 비밀번호 변경 강제
- [ ] 대시보드 메트릭 표시
- [ ] 로그 테이블 필터링
- [ ] 토큰 발급/폐기
- [ ] 미들웨어 토글

---

## 테스트 결과 요약

| # | 케이스 | 자동 검증 | 직접 필요 |
|---|--------|----------|----------|
| 1 | stdio 모드 | — | 인터랙티브 프로세스 |
| 2 | proxy 단일 upstream | **PASS** | — |
| 3 | proxy SSE | — | SSE upstream 필요 |
| 4 | Gateway 멀티 upstream | **PASS** | — |
| 5 | TLS (HTTPS) | **PASS** | — |
| 6 | 토큰 CLI | **PARTIAL** (CLI만 확인: 발급/목록/stats/폐기) | 토큰 MW 통합 (Bearer 포함 요청, 쿼터 초과) |
| 7 | Ed25519 서명 | — | 서명 도구 필요 |
| 8 | Web UI 키 CRUD | **PARTIAL** (API CRUD만 확인) | 등록된 키로 서명 인증 연동 |
| 9 | Web UI upstream CRUD | **PASS** | — |
| 10 | MW 토글 영속화 | **PARTIAL** (토글 동작만 확인) | 재시작 후 상태 유지 확인 |
| 11 | Rate Limit | **PASS** | — |
| 12 | IP 차단 | — | proxy IP 추출 확인 |
| 13 | DID blocklist | — | DID 서명 도구 필요 |
| 14 | 로그 조회 CLI | **PARTIAL** (CLI 실행만 확인) | 실제 로그 데이터 출력 확인 |
| 15 | SIGHUP 리로드 | — | 인터랙티브 |
| 16 | Prometheus 메트릭 | **PASS** | — |
| 17 | Web UI 대시보드 | — | 브라우저 필요 |
