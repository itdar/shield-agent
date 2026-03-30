# shield-agent 로컬 테스트 가이드 — HTTP API 인터셉트

에이전트가 외부 HTTP REST API를 호출할 때 shield-agent가 중간에서 인증·로깅하는 테스트 가이드.

> **현재 상태:** `internal/middleware/httpapi/` 패키지 구현 완료.
> HTTP API 전용 프록시 커맨드(`shield-agent api-proxy`)는 미구현 — 향후 추가 예정.

---

## 시나리오 개요

에이전트가 외부 REST API(GitHub API, Slack API, 사내 서비스 등)를 호출할 때
shield-agent가 리버스 프록시로 끼어들어 에이전트 신원 검증과 호출 로깅을 수행한다.

```
Agent
  │  POST /repos/owner/repo/issues  (GitHub API 호출)
  │  X-Agent-ID: agent-backend
  │  X-Agent-Signature: <hex sig>
  ▼
shield-agent api-proxy (:8890)  ← AuthMiddleware + LogMiddleware
  │  Authorization: Bearer <github_token>  (자격증명 주입 — 미래 기능)
  ▼
api.github.com (실제 API 서버)
```

---

## 서명 방식

HTTP API 미들웨어는 Ed25519 서명으로 에이전트 신원을 검증한다.

**서명 대상:** `sha256(method + " " + path + "\n" + body)`

예: `POST /repos/owner/repo/issues` 요청이라면:
```
sha256("POST /repos/owner/repo/issues\n{\"title\":\"bug\",...}")
```

**HTTP 헤더:**
- `X-Agent-ID`: 에이전트 ID (또는 `did:key:z...` DID)
- `X-Agent-Signature`: hex 인코딩된 Ed25519 서명

A2A와 헤더 이름이 유사하지만 시그니처 헤더 이름이 다르다:
| 프로토콜 | 서명 헤더 |
|---------|---------|
| A2A | `X-A2A-Signature` |
| HTTP API | `X-Agent-Signature` |

---

## 테스트 클라이언트 (Go)

`/tmp/httpapi_client_test.go`:

```go
package main

import (
    "bytes"
    "crypto/ed25519"
    "crypto/rand"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "net/http"
    "os"
)

func main() {
    // 1. 키 생성
    _, priv, _ := ed25519.GenerateKey(rand.Reader)
    agentID := "agent-backend"

    // 2. 요청 바디
    body := map[string]interface{}{
        "title": "test issue from agent",
        "body":  "automatically created by agent-backend",
    }
    bodyJSON, _ := json.Marshal(body)

    method := "POST"
    path := "/repos/testowner/testrepo/issues"

    // 3. 서명: sha256(method + " " + path + "\n" + body)
    h := sha256.New()
    fmt.Fprintf(h, "%s %s\n", method, path)
    h.Write(bodyJSON)
    hash := h.Sum(nil)
    sig := ed25519.Sign(priv, hash)

    // 4. shield-agent 프록시로 요청 (api.github.com 대신)
    target := "http://localhost:8890" + path
    req, _ := http.NewRequest(method, target, bytes.NewReader(bodyJSON))
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("X-Agent-ID", agentID)
    req.Header.Set("X-Agent-Signature", hex.EncodeToString(sig))

    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        fmt.Fprintf(os.Stderr, "error: %v\n", err)
        os.Exit(1)
    }
    defer resp.Body.Close()
    fmt.Printf("status: %d\n", resp.StatusCode)
}
```

```bash
go run /tmp/httpapi_client_test.go
```

---

## 간단한 에코 서버 (업스트림 목업)

실제 외부 API 대신 로컬 에코 서버로 테스트:

`/tmp/echo_server.py`:

```python
from http.server import HTTPServer, BaseHTTPRequestHandler
import json

class EchoHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(json.dumps({
            "echo": json.loads(body),
            "path": self.path,
        }).encode())

    def log_message(self, format, *args):
        pass  # suppress access log

if __name__ == "__main__":
    HTTPServer(("127.0.0.1", 9999), EchoHandler).serve_forever()
```

```bash
python3 /tmp/echo_server.py &
# 확인: curl -X POST http://localhost:9999/test -d '{"hello":"world"}'
```

---

## 키 등록

```yaml
# /tmp/keys.yaml
keys:
  - id: "agent-backend"
    key: "<base64 encoded ed25519 public key>"
```

---

## 설정 파일

`/tmp/shield-agent-httpapi.yaml`:

```yaml
server:
  monitor_addr: "127.0.0.1:9092"

security:
  mode: "open"           # 테스트: open / 운영: closed
  key_store_path: "/tmp/keys.yaml"

logging:
  level: "debug"
  format: "text"

telemetry:
  enabled: false

storage:
  db_path: "/tmp/shield-agent-httpapi.db"
  retention_days: 7
```

---

## 단위 테스트 실행

```bash
cd /Users/gino/Gin/src/RaaS/rua
go test ./internal/middleware/httpapi/...
```

---

## 동작 확인

### 인증 성공 로그
```
level=INFO  msg="HTTP API call verified" agent_id_hash=abc123... path=/repos/owner/repo/issues
```

### 인증 실패 로그 (closed 모드)
```
level=WARN  msg="HTTP API signature verification failed" agent_id_hash=abc123... path=/repos/owner/repo/issues
```

### 로그 조회 (method 컬럼 = "POST /path" 형식으로 기록됨)

```bash
/tmp/shield-agent logs \
  --config /tmp/shield-agent-httpapi.yaml \
  --last 20 \
  --format table
```

---

## 로그 레코드 형식

HTTP API 미들웨어는 `method` 컬럼을 `"HTTP_METHOD /path"` 형식으로 기록한다.
MCP JSON-RPC의 `tools/call` 같은 메서드명 대신 REST API 경로가 기록되어
어떤 API 엔드포인트가 얼마나 호출됐는지 추적 가능하다.

| 컬럼 | 예시 |
|------|------|
| method | `POST /repos/owner/repo/issues` |
| direction | `in` |
| agent_id_hash | `sha256(agent-backend)` |
| latency_ms | `142.3` |
| success | `true` |

---

## 트러블슈팅

**서명 검증 실패:**
- `X-Agent-Signature` 헤더가 누락되었거나 잘못된 hex 인코딩인지 확인
- 서명 대상 경로가 요청 URL path와 동일한지 확인 (query string 제외)

**업스트림 연결 실패:**
```bash
curl http://localhost:9999/test  # 에코 서버 동작 확인
```

**포트 충돌:**
```bash
lsof -i :8890
lsof -i :9999
```
