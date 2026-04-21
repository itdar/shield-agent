# Phase 2 잔여 Follow-up 가이드

Phase 5.2(egress MITM) 배포 전/중에 남아 있는 **외부 의존 항목** 3건을
다시 확인하기 쉽게 정리한 문서. 모두 코드 외부 작업이 주이며, 보수적인
최악의 시나리오에서만 경미한 shield-agent 코드 변경이 필요하다.

- 관련 설계 문서: `.omc/plans/egress-compliance.md` (RALPLAN-DR 합의안, Iteration 2 APPROVE)
- 관련 구현 커밋: `cb0bb8b` (Phase 1), `a70c93f` (Phase 2 + cleanup), `514370b` (docs)
- 설정 파일 스키마: `internal/config/config.go` — `EgressConfig`
- 런타임 결정 지점: `cmd/shield-agent/egress.go` — `startEgressListenerWithMetrics`

---

## 요약 표

| # | 항목 | 예상 코드 변경 | 주로 누가/어디서 |
|---|------|----------------|-----------------|
| 1 | 법무 검토 (TLS MITM의 PIPA 제23조 / GDPR Art. 9) | **0** (최악 시 ~20줄 startup gate) | 법무팀 검토 → 운영 런북 / 계약서 |
| 2 | AI기본법 시행령 / 시행규칙 확정 후 조항 라벨 갱신 | **0** | `.md` 문서 편집만 |
| 3 | Certificate pinning SDK 현황 확인 | **0** | `shield-agent.yaml` 한두 줄 이동 |

세 항목 모두 **shield-agent 소스를 다시 손볼 일은 거의 없음.** 운영자가
(a) 런북에 문장을 추가하거나 (b) `shield-agent.yaml` 에서 호스트 목록을
한 곳에서 다른 곳으로 옮기는 정도로 끝남.

---

## 1. 법무 검토 (TLS MITM의 법적 근거)

### 배경

Phase 2 의 `mitm_hosts` 는 TLS 본문을 복호화한다. 복호화된 평문에
건강정보, 성적 지향, 금융정보 같은 **민감정보** 또는 **특수 범주
데이터**가 포함될 경우 처리 주체 자격이 문제가 된다.

| 법령 | 조항 | 리스크 |
|------|------|--------|
| 한국 개인정보보호법 (PIPA) | 제23조 | 민감정보 처리 근거 필요 |
| EU GDPR | Art. 9 | 특수 범주 데이터 처리 금지 (예외 요건) |
| 통신비밀보호법 | 제3조 | 자사 관리 에이전트는 동의 전제로 합법, 다만 제3자 통신은 별도 |

### 검토 결과별 대응

| 검토 결과 | 필요한 작업 | 코드 변경 |
|-----------|-------------|-----------|
| 그대로 승인 | 없음 | 없음 |
| 조건부 승인 — "고객 동의 없이 `log_full_body: true` 금지" | 운영 런북 · 계약서 문구 추가 | 없음 |
| 조건부 승인 — "특정 지역/조직만 MITM 허용" | 해당 환경의 `mitm_hosts` 축소 | 없음 (config만) |
| 거부 | 해당 호스트는 `tls_passthrough_hosts` 로만 | 없음 (config만) |
| 강한 조건 — "감사 승인 없이 MITM 기동 금지" | startup gate 추가 | **~20줄** (config 검증) |

### 가장 보수적 시나리오의 startup gate 예시

```go
// internal/config/config.go validateEgress에 추가 가능
if len(e.MITMHosts) > 0 && os.Getenv("SHIELD_AGENT_LEGAL_APPROVED") != "true" {
    return fmt.Errorf("egress.mitm_hosts is set but SHIELD_AGENT_LEGAL_APPROVED=true is missing")
}
```

이 시나리오에서만 실제 코드 편집이 필요하고, 일반적인 "승인" 시나리오에서는
코드 변경이 없다.

### 운영 준비 체크리스트

- [ ] DPIA (Data Protection Impact Assessment) 또는 한국 AI기본법 영향평가 문서 작성
- [ ] 대상 에이전트 운영자와의 서면 동의 (TLS 본문 복호화에 대한 고지)
- [ ] `mitm_hosts` 화이트리스트와 사유를 문서화
- [ ] `log_full_body: false` 를 기본으로 유지하는 정책 확인
- [ ] 감사 로그 보존 기간이 규제 요구치 이상인지 확인 (`egress.retention_days`)

---

## 2. AI기본법 시행령 / 시행규칙 확정 후 조항 라벨 갱신

### 배경

2026-01-22 시행 인공지능 기본법의 구체 조항 번호는 본 문서 작성 시점 기준으로
시행령 / 시행규칙이 확정되지 않았다. Plan Section 2 에서 제23·27·34·35·37·40조로
추정 매핑을 두고 `(조항 확인 필요)` 태그를 달아두었다.

### 런타임 코드에 조항 번호가 있는가?

| 대상 | 조항 번호 하드코딩 여부 |
|------|-------------------------|
| `internal/compliance/export.go` (AuditBundle) | **없음** — 중립적 JSON |
| `internal/storage/egress.go` (스키마) | **없음** |
| Prometheus 메트릭 라벨 | **없음** |
| `internal/compliance/policy.go` | **없음** |

→ 런타임 코드는 조항 번호에 비종속. docs 편집으로 충분.

### 갱신 대상 파일

- `.omc/plans/egress-compliance.md` Section 2 (`(조항 확인 필요)` 제거)
- `README.md` — "Regulatory mapping" 표
- `README.ko.md` — "규제 의무 매핑" 표
- 본 문서(`docs/egress-phase2-followups.md`)

### 규제 기관이 구조화된 조항 필드를 요구하는 예외 시나리오

만약 감사 번들에 row 별 조항 매핑 필드를 요구한다면 (예시):

```json
{
  "logs": [
    {
      "id": 1234,
      "required_by_articles": ["23", "27"],
      ...
    }
  ]
}
```

이 경우 `internal/storage/egress.go` 의 `EgressLog` 에 필드 추가 +
`internal/compliance/hashchain.go` 의 `canonicalRowHash` 에 필드 추가 +
마이그레이션 12 가 필요하다. 현재 상태는 "raw data + hash proof" 구조이므로
규제 기관이 외부에서 재해석 가능해 기본적으로 불필요.

### 운영 준비 체크리스트

- [ ] 시행령 / 시행규칙 확정 시점 모니터링
- [ ] 확정 후 4개 `.md` 파일의 매핑 표 갱신
- [ ] 규제 기관이 추가 구조화 요구 시 스키마 확장 (`migration 12` + `canonicalRowHash` 동시 갱신 필수)

---

## 3. Certificate Pinning SDK 현황 확인

### 배경

Certificate pinning 은 SDK 가 특정 인증서(혹은 공개키 지문)만 하드코딩해
OS trust store 내용과 무관하게 나머지를 전부 거부하는 기법.
Phase 2 MITM 은 런타임에 shield-agent CA 로 서명한 leaf 인증서를 끼워 넣는
방식이므로 pinning SDK 는 TLS handshake 를 거부한다.

### 확인 대상

Phase 2 배포 직전 대상 에이전트에서 사용 중인 서버사이드 LLM SDK 의 최신
버전이 pinning 을 사용하는지 확인. 일반적인 현황:

| SDK | 현재 상태 (알려진 버전 기준) | MITM 가능 |
|-----|-------------------------------|-----------|
| OpenAI Python / Node SDK | httpx / fetch + OS CA 신뢰 | 가능 |
| Anthropic Python / Node SDK | httpx / fetch + OS CA 신뢰 | 가능 |
| Google `google-genai` / REST | OS CA 신뢰 | 가능 |
| 모바일 네이티브 SDK (iOS/Android) | 종종 pinning | **불가 — 대부분 `tls_passthrough_hosts`** |

### 발견 결과별 대응

| 결과 | 대응 |
|------|------|
| 대상 SDK 모두 pinning 안 함 | `mitm_hosts` 그대로 활성화 |
| 특정 SDK 만 pinning | 해당 호스트를 `tls_passthrough_hosts` 로 이동 |
| 모든 SDK 가 pinning | Phase 1 metadata-only 운영 |

### 설정 예시

```yaml
egress:
  mitm_hosts:
    - "api.openai.com"          # pinning 안 함 → MITM OK
    - "api.anthropic.com"       # pinning 안 함 → MITM OK

  tls_passthrough_hosts:
    - "api.pinning-sdk.com"     # pinning 사용 → metadata-only 로 강등
```

코드 변경은 **0**. `shield-agent.yaml` 한두 줄 이동으로 해결.

### 운영 준비 체크리스트

- [ ] Phase 2 활성화 전 타겟 SDK 의 "최신 릴리스 노트 + 보안 섹션" 확인
- [ ] pinning 사용 SDK 가 있으면 `tls_passthrough_hosts` 로 이동
- [ ] MITM 실패 시 자동 fallback 은 **없음** — 운영자가 로그에서 `x509: certificate signed by unknown authority` 모니터링하여 수동으로 옮겨야 함
- [ ] `shield-agent logs --direction egress --last 20` 로 `error_detail` 에 TLS 에러가 반복되는지 주기 확인

---

## 부록: 관련 파일 Quick Reference

- `.omc/plans/egress-compliance.md` — 원본 RALPLAN-DR 합의안
- `shield-agent.example.yaml` — `egress:` 섹션 예시 (주석 포함)
- `internal/config/config.go` — `EgressConfig`, `validateEgress`
- `cmd/shield-agent/egress.go` — `startEgressListenerWithMetrics` (MITM 분기, digest writer 기동)
- `internal/egress/mitm.go` — `MITMMinter`, leaf 인증서 캐시
- `internal/egress/ca.go` — `LoadOrGenerate`, `GenerateCA`
- `internal/compliance/pii_scrub.go` — 한국 특화 PII 정규식
- `internal/compliance/content_tag.go` — provider → model 추출기 + fallback probe
- `internal/compliance/hashchain.go` — 해시 체인 writer/verifier + 앵커 처리
