# TODO — 수동 작업 목록

## 핵심 문제: 저장소가 Private

현재 `itdar/shield-agent`가 **private 저장소**입니다.
이로 인해 릴리스 에셋 다운로드(404), Docker pull(unauthorized), curl 설치(404) 모두 실패합니다.

### 선택지 A: 저장소를 Public으로 전환 (권장)

```
GitHub → itdar/shield-agent → Settings → General → Danger Zone → Change visibility → Public
```

Public 전환 후 모든 설치 방법이 즉시 동작합니다.

### 선택지 B: Private 유지 + 토큰 기반 접근

Private 유지 시, 모든 다운로드에 GitHub 토큰이 필요합니다:
```bash
# curl 설치 (토큰 필요)
GITHUB_TOKEN=ghp_xxx curl -sSL https://raw.githubusercontent.com/itdar/shield-agent/main/scripts/install.sh | sh

# Docker (ghcr.io 로그인 필요)
echo $GITHUB_TOKEN | docker login ghcr.io -u itdar --password-stdin
docker pull ghcr.io/itdar/shield-agent:0.1.0

# Homebrew는 private repo에서 동작하지 않음 → go install 사용
GONOSUMCHECK=github.com/itdar/* GOPRIVATE=github.com/itdar/* go install github.com/itdar/shield-agent/cmd/shield-agent@v0.1.0
```

---

## 1. GitHub Release 확인 및 재실행

릴리스 workflow가 실행됐는지 확인:

```bash
# GitHub Actions 페이지에서 확인
# https://github.com/itdar/shield-agent/actions/workflows/release.yml

# 또는 gh CLI로
gh run list --workflow=release.yml
```

**릴리스가 실패한 경우** (goreleaser 에러 등):
```bash
# 기존 태그 삭제 후 재생성
git tag -d v0.1.0
git push origin :refs/tags/v0.1.0

# 코드 수정 후 (이미 수정됨: goreleaser v1 고정, install script 에러처리)
git push origin main
git tag v0.1.0
git push origin v0.1.0
```

**릴리스가 성공했지만 Private라 접근 불가한 경우**:
→ 선택지 A (Public 전환) 실행

---

## 2. Docker 이미지 공개 설정 (Private 유지 시)

GitHub 패키지는 기본 private입니다. 공개하려면:

```
GitHub → itdar/shield-agent → Packages (우측 사이드바)
→ shield-agent 패키지 클릭 → Package settings → Danger Zone
→ Change visibility → Public
```

테스트:
```bash
docker pull ghcr.io/itdar/shield-agent:0.1.0
docker run --rm ghcr.io/itdar/shield-agent:0.1.0 --help
```

---

## 3. Homebrew tap 설정 (Public 전환 후)

```bash
# 1. homebrew-tap 저장소 생성
gh repo create itdar/homebrew-tap --public --description "Homebrew formulae"

# 2. 클론
git clone https://github.com/itdar/homebrew-tap.git /tmp/homebrew-tap

# 3. Formula 복사
cp Formula/shield-agent.rb /tmp/homebrew-tap/shield-agent.rb

# 4. sha256 업데이트 — 릴리스의 checksums.txt에서 확인
gh release download v0.1.0 --pattern "checksums.txt" --dir /tmp
cat /tmp/checksums.txt
# 각 플랫폼별 sha256 값으로 Formula의 REPLACE_WITH_ACTUAL_SHA256_* 교체

# 5. 커밋 & 푸시
cd /tmp/homebrew-tap
git add shield-agent.rb
git commit -m "Add shield-agent 0.1.0"
git push

# 6. 테스트
brew tap itdar/tap
brew install shield-agent
shield-agent --help
```

---

## 4. curl 설치 테스트 (Public 전환 후)

```bash
curl -sSL https://raw.githubusercontent.com/itdar/shield-agent/main/scripts/install.sh | sh
shield-agent --help
```

Private 상태에서 테스트:
```bash
GITHUB_TOKEN=ghp_xxx curl -sSL https://raw.githubusercontent.com/itdar/shield-agent/main/scripts/install.sh | sh
```

---

## 5. 전체 설치 방법 테스트 체크리스트

| 방법 | 명령어 | Public 필요 |
|------|--------|-------------|
| go install | `go install github.com/itdar/shield-agent/cmd/shield-agent@v0.1.0` | No (GOPRIVATE 설정 시) |
| curl | `curl -sSL .../install.sh \| sh` | Yes (또는 GITHUB_TOKEN) |
| Docker | `docker pull ghcr.io/itdar/shield-agent:0.1.0` | Yes (또는 docker login) |
| Homebrew | `brew tap itdar/tap && brew install shield-agent` | Yes |
| 소스 빌드 | `go build ./cmd/shield-agent` | No |

---

## 6. ROADMAP Phase 2 잔여 — 문서

- [ ] CONTRIBUTING.md 작성
- [ ] CODE_OF_CONDUCT.md 작성
- [ ] docs/ 내 mermaid 아키텍처 다이어그램 추가
