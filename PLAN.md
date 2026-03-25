# PLAN.md — 실행 계획

> ROADMAP.md Phase 1 (Core MVP) 구현을 위한 체크포인트별 실행 계획.
> 각 체크포인트는 **빌드 통과 + 테스트 통과** 상태로 커밋한다.

---

## 완료된 체크포인트 (CP-0 ~ CP-10)

### CP-0: 프로젝트 기반 정리 ✅ `ec7f0bd`

- [x] 불필요 파일 삭제 (LOCAL_TEST*.md, REAL_TEST.md, mcp-shield, .idea/)
- [x] `.gitignore` 업데이트
- [x] `go.mod`: `module rua` → `module github.com/itdar/shield-agent`
- [x] 전체 import path 일괄 변경 (20개 파일)
- [x] 빌드 + 테스트 통과

### CP-1: 네이밍 통일 ✅ `9731024`

- [x] 환경변수: `MCP_SHIELD_*` → `SHIELD_AGENT_*`
- [x] Prometheus: `mcp_shield_*` → `shield_agent_*`
- [x] example YAML, 테스트 파일 업데이트

### CP-2: 주석 영어화 ✅ `5e06a74`

- [x] 전체 Go 파일 한글 주석 → 영어 (0 잔여)

### CP-3: Middleware 리팩토링 ✅ `fe87773`

- [x] `Name()` 메서드 추가
- [x] `MiddlewareEntry` 설정 구조체
- [x] `registry.go` — `BuildChain()` 팩토리
- [x] `--disable-middleware` / `--enable-middleware` CLI 플래그
- [x] 하드코딩 체인 → YAML 기반 동적 구성

### CP-4: Guard Middleware ✅ `346c174`

- [x] Rate limiter (fixed window, method별)
- [x] Request size limit
- [x] IP blocklist / allowlist (CIDR)
- [x] `shield_agent_rate_limit_rejected_total` 메트릭
- [x] 테스트 6개 작성

### CP-5+6: A2A/HTTPAPI 통합 + TLS/CORS ✅ `9a47922`

- [x] `internal/middleware/httpauth` 공통 패키지 추출
- [x] A2A, HTTPAPI auth 중복 제거
- [x] TLS: `--tls-cert`, `--tls-key` 플래그 + config
- [x] CORS: `cors_allowed_origins` 설정 기반 (하드코딩 제거)

### CP-7+8: SIGHUP 리로드 + 메트릭 보강 ✅ `f4971af`

- [x] `SwappableChain` — atomic 체인 교체
- [x] SIGHUP 핸들러 (stdio + proxy 모두)
- [x] LogMiddleware → Prometheus 카운터/히스토그램 연동
- [x] proxy 모드 `/healthz` upstream 헬스체크

### CP-9: README 업데이트 ✅ `f583c94`

- [x] 전체 문서 최신화

### CP-10: 전체 검증 ✅

- [x] `go vet ./...` — 0 errors
- [x] `go build ./cmd/shield-agent` — 성공
- [x] `go test ./... -v -count=1` — all PASS (14 packages)
- [x] `go test -race ./...` — race condition 없음

---

## 완료된 항목 — Phase 1 잔여 (모두 완료)

### CP-12: CI/CD + 빌드 자동화 ✅

- [x] GitHub Actions 워크플로우 (`.github/workflows/ci.yml`)
  - build, test, lint (`go vet`), race detection
  - PR / push to main 트리거
- [x] `.goreleaser.yml` 설정
  - Linux/macOS/Windows 바이너리 크로스 컴파일
  - Docker image 빌드

### CP-13: Guard 고도화 ✅

- [x] **Brute force 방어**: 연속 실패 N회 시 자동 임시 차단
- [x] **비정상 페이로드 감지**: malformed JSON-RPC 차단
- [x] Guard IP 차단 테스트 작성

### CP-14: Log Middleware 보강 ✅

- [x] `ip_address` 컬럼 추가 (action_logs 스키마, migration v2)
- [x] ActionLog 구조체 및 Insert/Query 업데이트

### CP-15: Storage 고도화 ✅

- [x] DB 마이그레이션 시스템 (schema_versions 테이블, 버전 관리)
  - 스키마 변경 시 자동 마이그레이션

### CP-16: 추가 테스트 ✅

- [x] transport: proxy 포워딩 테스트 (SSE, Streamable HTTP) — 11개 테스트
- [x] httpauth: 공통 인증 로직 단위 테스트 — 8개 테스트
- [x] guard: IP 차단/허용, brute force, malformed JSON-RPC 테스트 — 6개 테스트

### CP-17: 실제 사용자 테스트 문서 ✅

- [x] `docs/testing-guide.md` 작성
  - stdio 모드로 MCP 서버 래핑
  - proxy 모드로 외부 MCP 서버 프록시
  - 로그 조회 CLI 사용법
  - rate limit 동작 확인
  - SIGHUP 리로드 확인
  - TLS 모드, 모니터링 엔드포인트

---

## 체크포인트 순서 요약

```
완료:
  CP-0   프로젝트 클린업          ✅ ec7f0bd
  CP-1   네이밍 통일              ✅ 9731024
  CP-2   주석 영어화              ✅ 5e06a74
  CP-3   Middleware 리팩토링      ✅ fe87773
  CP-4   Guard Middleware         ✅ 346c174
  CP-5+6 A2A 통합 + TLS/CORS     ✅ 9a47922
  CP-7+8 SIGHUP + 메트릭         ✅ f4971af
  CP-9   README 업데이트          ✅ f583c94
  CP-10  전체 검증                ✅ (빌드/테스트/race 통과)

완료 (Phase 1 잔여 — 모두 완료):
  CP-12  CI/CD + 빌드 자동화        ✅
  CP-13  Guard 고도화               ✅
  CP-14  Log ip_address 컬럼        ✅
  CP-15  DB 마이그레이션 시스템      ✅
  CP-16  추가 테스트 (proxy, httpauth) ✅
  CP-17  사용자 테스트 문서          ✅
```
