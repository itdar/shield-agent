# mcp-shield 로컬 테스트 가이드 — A2A (Agent-to-Agent)

Google A2A Protocol 기반 에이전트 간 통신 인터셉트 테스트 가이드.

> **현재 상태:** `internal/middleware/a2a/` 패키지 구현 완료.
> A2A 전용 프록시 커맨드(`mcp-shield a2a-proxy`)는 미구현 — 향후 추가 예정.

---

## A2A 프로토콜 개요

Google A2A는 에이전트 간 통신을 위한 HTTP 기반 JSON-RPC 2.0 프로토콜이다.

```
Agent A
  │  POST / (JSON-RPC body)
  │  X-Agent-ID: agent-a
  │  X-A2A-Signature: <hex sig>
  ▼
mcp-shield a2a-proxy  ← AuthMiddleware + LogMiddleware
  │
  ▼
Agent B (A2A server)
```

| A2A 메서드 | 설명 |
|-----------|------|
| `tasks/send` | 새 태스크 전송 |
| `tasks/get` | 태스크 상태 조회 |
| `tasks/cancel` | 태스크 취소 |
| `tasks/sendSubscribe` | SSE 스트리밍으로 태스크 전송 |
| `tasks/resubscribe` | 기존 SSE 스트림 재연결 |

---

## 서명 방식

A2A 미들웨어는 Ed25519 서명으로 에이전트 신원을 검증한다.

**서명 대상:** `sha256(method + " " + path + "\n" + body)`

예: `POST /` 요청에 body `{"jsonrpc":"2.0","method":"tasks/send",...}` 가 있으면:
```
sha256("POST /\n{\"jsonrpc\":\"2.0\",\"method\":\"tasks/send\",...}")
```

**HTTP 헤더:**
- `X-Agent-ID`: 에이전트 ID (또는 `did:key:z...` DID)
- `X-A2A-Signature`: hex 인코딩된 Ed25519 서명

---

## 테스트 클라이언트 (Go)

`/tmp/a2a_client_test.go`:

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
    // 1. 키 생성 (실제 운영에서는 파일에서 로드)
    pub, priv, _ := ed25519.GenerateKey(rand.Reader)
    _ = pub
    agentID := "agent-test-1"

    // 2. A2A 요청 바디 구성
    body := map[string]interface{}{
        "jsonrpc": "2.0",
        "id":      1,
        "method":  "tasks/send",
        "params": map[string]interface{}{
            "id": "task-001",
            "message": map[string]interface{}{
                "role": "user",
                "parts": []map[string]string{
                    {"type": "text", "text": "hello from agent"},
                },
            },
        },
    }
    bodyJSON, _ := json.Marshal(body)

    // 3. 서명 생성: sha256(method + " " + path + "\n" + body)
    h := sha256.New()
    fmt.Fprintf(h, "POST /\n")
    h.Write(bodyJSON)
    hash := h.Sum(nil)
    sig := ed25519.Sign(priv, hash)

    // 4. HTTP 요청 전송
    req, _ := http.NewRequest("POST", "http://localhost:8889/", bytes.NewReader(bodyJSON))
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("X-Agent-ID", agentID)
    req.Header.Set("X-A2A-Signature", hex.EncodeToString(sig))

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
go run /tmp/a2a_client_test.go
```

---

## 키 등록

`/tmp/keys.yaml`에 에이전트 공개키 추가:

```bash
# 공개키 base64 출력 (위 클라이언트 코드에서 pub 출력하도록 수정)
go run /tmp/print_pubkey.go
```

```yaml
# /tmp/keys.yaml
keys:
  - id: "agent-test-1"
    key: "<base64 encoded ed25519 public key>"
```

---

## 설정 파일

`/tmp/mcp-shield-a2a.yaml`:

```yaml
server:
  monitor_addr: "127.0.0.1:9091"

security:
  mode: "open"           # 테스트: open / 운영: closed
  key_store_path: "/tmp/keys.yaml"

logging:
  level: "debug"
  format: "text"

telemetry:
  enabled: false

storage:
  db_path: "/tmp/mcp-shield-a2a.db"
  retention_days: 7
```

---

## 단위 테스트 실행

```bash
cd /Users/gino/Gin/src/RaaS/rua
go test ./internal/middleware/a2a/...
```

---

## 동작 확인

### 인증 성공 로그 (open 모드)
```
level=WARN  msg="unsigned A2A request" method=POST path=/
level=INFO  msg="A2A request verified" agent_id_hash=abc123... path=/
```

### 인증 실패 로그 (closed 모드)
```
level=WARN  msg="A2A signature verification failed" agent_id_hash=abc123... path=/
```

### 로그 조회

```bash
/tmp/mcp-shield logs \
  --config /tmp/mcp-shield-a2a.yaml \
  --last 20 \
  --format table
```

---

## 트러블슈팅

**서명 검증 실패:**
- 서명 대상이 정확히 `sha256(method + " " + path + "\n" + body)` 인지 확인
- 바디를 읽은 후 재사용하는지 확인 (스트림은 한 번만 읽힘)

**키를 못 찾는 경우:**
- `keys.yaml`에 에이전트 ID가 등록되어 있는지 확인
- `did:key:` 형식인 경우 Ed25519 multicodec prefix(0xed01)가 맞는지 확인
