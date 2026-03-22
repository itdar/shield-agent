# mcp-shield 로컬 테스트 가이드

프로토콜별로 분리된 테스트 가이드:

| 파일 | 프로토콜 | 상태 |
|------|---------|------|
| [LOCAL_TEST_JSONRPC.md](LOCAL_TEST_JSONRPC.md) | MCP JSON-RPC (stdio / SSE / Streamable HTTP) | 구현 완료 |
| [LOCAL_TEST_A2A.md](LOCAL_TEST_A2A.md) | Google A2A (Agent-to-Agent, HTTP JSON-RPC) | 미들웨어 구현 완료 / 프록시 커맨드 미구현 |
| [LOCAL_TEST_HTTPAPI.md](LOCAL_TEST_HTTPAPI.md) | HTTP API 인터셉트 (Agent → REST API) | 미들웨어 구현 완료 / 프록시 커맨드 미구현 |
