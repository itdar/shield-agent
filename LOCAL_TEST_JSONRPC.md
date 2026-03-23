# mcp-shield 로컬 테스트 가이드 — MCP JSON-RPC

MCP JSON-RPC 프로토콜(stdio / SSE / Streamable HTTP)에 대한 로컬 테스트 가이드.

| 모드 | 언제 쓰나 | 명령어 |
|------|-----------|--------|
| **HTTP Proxy** | fastmcp 등이 이미 HTTP로 돌고 있을 때 앞에 끼어들기 | `mcp-shield proxy --upstream ...` |
| **stdio Wrapping** | MCP 서버를 직접 띄우면서 stdio를 가로채기 | `mcp-shield python3 server.py` |

---

## 사전 준비

```bash
pip install fastmcp

# mcp-shield 빌드
cd /Users/gino/Gin/src/RaaS/rua
go build -o /tmp/mcp-shield ./cmd/mcp-shield
```

---

## 방식 1: HTTP Proxy 모드

이미 돌고 있는 fastmcp 서버 앞에 mcp-shield를 끼워 넣는다.

```
Claude Desktop
      │ SSE 또는 Streamable HTTP (:8888)
      ▼
mcp-shield proxy  ← 인증·로깅 적용
      │
      ▼
fastmcp 서버 (:8000, 이미 실행 중)
```

### 1-1. fastmcp 서버 실행

`/tmp/my_server.py`:

```python
from fastmcp import FastMCP

mcp = FastMCP("로컬 테스트 서버")

@mcp.tool()
def hello(name: str) -> str:
    """인사를 반환한다"""
    return f"안녕하세요, {name}!"

@mcp.tool()
def add(a: int, b: int) -> int:
    """두 수를 더한다"""
    return a + b

if __name__ == "__main__":
    mcp.run(transport="sse", host="127.0.0.1", port=8000)
```

```bash
python3 /tmp/my_server.py
# 확인: curl -N http://localhost:8000/sse
```

### 1-2. mcp-shield proxy 실행

```bash
# simple example (streamable-http)
/tmp/mcp-shield proxy --upstream http://localhost:8000

# SSE transport
/tmp/mcp-shield proxy \
  --config /tmp/mcp-shield-local.yaml \
  --listen :8888 \
  --upstream http://localhost:8000 \
  --transport sse

# Streamable HTTP transport (서버가 streamable-http 모드인 경우)
/tmp/mcp-shield proxy \
  --config /tmp/mcp-shield-local.yaml \
  --listen :8888 \
  --upstream http://localhost:8000 \
  --transport streamable-http
```

### 1-3. Claude Desktop 설정

-> TODO: 이거는 클로드데스크톱 앱에서 커넥터 연결하려면 https:// 여야해서 ngrok 써서 커넥터 등록하는 방법으로 해라.
```json
{
  "mcpServers": {
    "local-fastmcp": {
      "url": "http://localhost:8888/sse"
    }
  }
}
```

Streamable HTTP인 경우:
```json
{
  "mcpServers": {
    "local-fastmcp": {
      "url": "http://localhost:8888/mcp",
      "type": "streamable-http"
    }
  }
}
```

---

## 방식 2: stdio Wrapping 모드

mcp-shield가 MCP 서버를 **직접 자식 프로세스로 실행**하고 stdin/stdout을 가로챈다.
별도 포트 불필요. Claude Desktop이 stdio 방식으로 mcp-shield를 실행하면 mcp-shield가 실제 서버를 띄운다.

```
Claude Desktop
      │ stdio (command 실행)
      ▼
mcp-shield  ← 인증·로깅 적용
      │ stdin/stdout 파이프
      ▼
python3 server.py (자식 프로세스)
```

### 2-1. fastmcp 서버 (stdio 모드)

`/tmp/my_server_stdio.py`:

```python
from fastmcp import FastMCP

mcp = FastMCP("로컬 테스트 서버")

@mcp.tool()
def hello(name: str) -> str:
    return f"안녕하세요, {name}!"

@mcp.tool()
def add(a: int, b: int) -> int:
    return a + b

if __name__ == "__main__":
    mcp.run(transport="stdio")  # stdio 모드
```

### 2-2. Claude Desktop 설정

Claude Desktop이 mcp-shield를 직접 실행하고, mcp-shield가 python을 띄운다:

```json
{
  "mcpServers": {
    "local-wrapped": {
      "command": "/tmp/mcp-shield",
      "args": [
        "--config", "/tmp/mcp-shield-local.yaml",
        "python3", "/tmp/my_server_stdio.py"
      ]
    }
  }
}
```

### 2-3. 터미널에서 직접 테스트

```bash
/tmp/mcp-shield \
  --config /tmp/mcp-shield-local.yaml \
  python3 /tmp/my_server_stdio.py

# 그 다음 stdin에 직접 JSON-RPC 입력:
{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}
```

---

## 설정 파일

`/tmp/mcp-shield-local.yaml`:

```yaml
server:
  monitor_addr: "127.0.0.1:9090"

security:
  mode: "open"           # 테스트용 — 서명 없어도 통과
  key_store_path: "/tmp/keys.yaml"

logging:
  level: "debug"
  format: "text"

telemetry:
  enabled: false

storage:
  db_path: "/tmp/mcp-shield-local.db"
  retention_days: 7
```

---

## 서명 테스트 (closed 모드)

Ed25519 서명이 제대로 검증되는지 확인하려면:

```go
// /tmp/sign_test.go
package main

import (
    "crypto/ed25519"
    "crypto/rand"
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
)

func main() {
    pub, priv, _ := ed25519.GenerateKey(rand.Reader)
    fmt.Printf("public key (base64): %s\n", encodeBase64(pub))

    // 서명할 페이로드 구성
    type payload struct {
        Method string          `json:"method"`
        Params json.RawMessage `json:"params"`
    }
    params := map[string]string{"_mcp_agent_id": "agent-1"}
    paramsJSON, _ := json.Marshal(params)
    p := payload{Method: "tools/list", Params: paramsJSON}
    b, _ := json.Marshal(p)
    h := sha256.Sum256(b)

    sig := ed25519.Sign(priv, h[:])
    fmt.Printf("signature: %s\n", hex.EncodeToString(sig))
}
```

---

## 동작 확인 (공통)

### 로그 조회

```bash
/tmp/mcp-shield logs \
  --config /tmp/mcp-shield-local.yaml \
  --last 20 \
  --format table
```

### 모니터링 (HTTP Proxy 모드에서만)

```bash
curl -s http://localhost:9090/healthz | python3 -m json.tool
curl -s http://localhost:9090/metrics | grep mcp_shield
```

### 정상 동작 시 콘솔 로그 예시

**HTTP Proxy:**
```
level=INFO  msg="sse: session started" session_id=abc-123
level=WARN  msg="unsigned request" method=tools/list
```

**stdio Wrapping:**
TODO: 이거는 child process started 까지 정상 뜬 다음에 밑에 그냥 fastmcp 접속 로그만 뜨고 shield-mcp 로그는 안뜨던데?
```
level=INFO  msg="child process started" command=python3 pid=12345
level=WARN  msg="unsigned request" method=tools/list
```

---

## 트러블슈팅

**HTTP Proxy — upstream unavailable:**
```bash
curl -N http://localhost:8000/sse   # fastmcp 실행 중인지 확인
```

**stdio Wrapping — 서버가 안 뜸:**
```bash
# mcp-shield 없이 직접 실행해서 오류 확인
python3 /tmp/my_server_stdio.py
```

**포트 충돌:**
```bash
lsof -i :8888
lsof -i :8000
```
