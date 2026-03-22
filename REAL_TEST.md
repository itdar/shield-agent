# mcp-shield 실서버(클라우드) 테스트 가이드

클라우드에 Docker로 fastmcp가 외부 도메인으로 실행 중일 때 mcp-shield를 끼워 넣는 방법.
로컬과 마찬가지로 두 가지 모드 모두 사용 가능하다.

| 모드 | 구조 | 언제 쓰나 |
|------|------|-----------|
| **HTTP Proxy** | 로컬 또는 클라우드에 mcp-shield를 별도 포트로 띄움 | 클라우드 MCP가 HTTP(S)로 이미 돌고 있을 때 |
| **stdio Wrapping** | mcp-shield가 MCP 서버를 자식 프로세스로 직접 실행 | SSH 접속 가능하거나 같은 서버에 있을 때 |

---

## 방식 1: HTTP Proxy 모드

### 시나리오 A — 로컬에서 클라우드로 연결

```
Claude Desktop (로컬)
      │ SSE 또는 Streamable HTTP
      ▼
mcp-shield proxy  (localhost:8888, 로컬 실행)  ← 인증·로깅
      │ HTTPS
      ▼
fastmcp 서버  (https://mcp.example.com, 클라우드 Docker)
```

```bash
# 클라우드 서버 접근 확인
curl -N https://mcp.example.com/sse

# mcp-shield 로컬 실행
mcp-shield proxy \
  --config ~/mcp-shield-real.yaml \
  --listen :8888 \
  --upstream https://mcp.example.com \
  --transport sse          # 또는 streamable-http
```

Claude Desktop 설정:
```json
{
  "mcpServers": {
    "cloud-mcp": {
      "url": "http://localhost:8888/sse"
    }
  }
}
```

---

### 시나리오 B — 클라우드에 mcp-shield도 함께 배포

```
Claude Desktop (로컬)
      │ HTTPS
      ▼
mcp-shield proxy  (https://proxy.example.com, 클라우드)  ← 인증·로깅
      │ 내부 네트워크
      ▼
fastmcp 서버  (컨테이너 내부)
```

`docker-compose.yml` (클라우드 서버):

```yaml
services:
  fastmcp:
    image: python:3.12-slim
    command: >
      sh -c "pip install fastmcp && python /app/server.py"
    volumes:
      - ./server.py:/app/server.py
    expose:
      - "8000"   # 외부 노출 안 함

  mcp-shield:
    build:
      context: .
      dockerfile: Dockerfile.mcp-shield
    command: >
      mcp-shield proxy
        --config /etc/mcp-shield/config.yaml
        --listen :8888
        --upstream http://fastmcp:8000
        --transport sse
    volumes:
      - ./mcp-shield-config.yaml:/etc/mcp-shield/config.yaml
      - mcp-shield-data:/data
    ports:
      - "8888:8888"
    depends_on:
      - fastmcp

volumes:
  mcp-shield-data:
```

`Dockerfile.mcp-shield`:
```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY . .
RUN go build -o /mcp-shield ./cmd/mcp-shield

FROM alpine:3.19
COPY --from=builder /mcp-shield /usr/local/bin/mcp-shield
ENTRYPOINT ["mcp-shield"]
```

Claude Desktop 설정 (nginx/TLS 앞단 가정):
```json
{
  "mcpServers": {
    "cloud-mcp": {
      "url": "https://proxy.example.com/sse"
    }
  }
}
```

---

## 방식 2: stdio Wrapping 모드

클라우드 서버에 SSH 접속이 가능하거나, 같은 서버에서 MCP 서버를 직접 실행할 때.
mcp-shield가 MCP 서버를 자식 프로세스로 띄우고 stdio를 가로챈다.

```
Claude Desktop (로컬, SSH 터널 또는 직접 실행)
      │ stdio
      ▼
mcp-shield  (클라우드 서버에서 실행)  ← 인증·로깅
      │ stdin/stdout 파이프
      ▼
python3 server.py (자식 프로세스)
```

### Claude Desktop에서 SSH를 통한 stdio 실행

```json
{
  "mcpServers": {
    "cloud-wrapped": {
      "command": "ssh",
      "args": [
        "user@mcp.example.com",
        "mcp-shield --config /etc/mcp-shield/config.yaml python3 /app/server.py"
      ]
    }
  }
}
```

### 클라우드 서버에 mcp-shield 설치

```bash
# 클라우드 서버에서 빌드
git clone <repo> rua && cd rua
go build -o /usr/local/bin/mcp-shield ./cmd/mcp-shield

# 또는 미리 빌드한 바이너리 업로드
scp /tmp/mcp-shield user@mcp.example.com:/usr/local/bin/
```

---

## 설정 파일 (클라우드용)

`mcp-shield-config.yaml`:

```yaml
server:
  monitor_addr: "127.0.0.1:9090"   # 클라우드에서 외부 노출 원하면 0.0.0.0

security:
  mode: "closed"                    # 클라우드는 closed 권장
  key_store_path: "/etc/mcp-shield/keys.yaml"

logging:
  level: "info"
  format: "json"                    # 로그 수집기 연동 시 json

telemetry:
  enabled: false

storage:
  db_path: "/data/mcp-shield.db"
  retention_days: 30
```

---

## fastmcp Docker 이미지

`server.py`:

```python
from fastmcp import FastMCP

mcp = FastMCP("클라우드 MCP 서버")

@mcp.tool()
def hello(name: str) -> str:
    return f"안녕하세요 from cloud, {name}!"

if __name__ == "__main__":
    # HTTP Proxy 방식이면 sse 또는 streamable-http
    mcp.run(transport="sse", host="0.0.0.0", port=8000)

    # stdio Wrapping 방식이면:
    # mcp.run(transport="stdio")
```

`Dockerfile`:
```dockerfile
FROM python:3.12-slim
WORKDIR /app
RUN pip install fastmcp
COPY server.py .
CMD ["python", "server.py"]
```

---

## 동작 확인

### 로그 조회

```bash
# HTTP Proxy 모드 — mcp-shield가 돌고 있는 서버에서
mcp-shield logs --config /etc/mcp-shield/config.yaml --last 50 --format table

# 최근 1시간 필터
mcp-shield logs --config /etc/mcp-shield/config.yaml --since 1h --format json
```

### 모니터링

```bash
# mcp-shield 서버 내부에서
curl -s http://localhost:9090/healthz | python3 -m json.tool
curl -s http://localhost:9090/metrics | grep mcp_shield
```

---

## security mode: closed 전환

처음엔 `open`으로 동작 확인 후 `closed`로 변경:

```yaml
security:
  mode: "closed"
  key_store_path: "/etc/mcp-shield/keys.yaml"
```

`keys.yaml`:
```yaml
keys:
  - id: "my-agent"
    key: "<base64 Ed25519 공개키>"
```

키 생성은 `LOCAL_TEST.md` 참고.

---

## 트러블슈팅

**TLS 인증서 오류 (자체 서명):**
- Let's Encrypt 등 공인 인증서 사용 권장
- 테스트 시 `HTTP_PROXY`나 인증서 번들 설정 필요

**SSE 연결이 nginx 뒤에서 끊김:**
```nginx
# nginx 설정 필수
proxy_buffering off;
proxy_read_timeout 3600s;
proxy_set_header Connection '';
chunked_transfer_encoding on;
```

**Claude Desktop HTTPS 연결 거부:**
- 자체 서명 인증서는 Claude Desktop이 신뢰 안 함
- 공인 인증서 또는 로컬 CA 등록 필요
