# TODO — 수동 작업 목록

## 1. GitHub CLI 인증

```bash
gh auth login
```

- 브라우저 인증 또는 토큰 입력

## 2. GitHub Actions Release 확인

```bash
# 릴리스 워크플로우 상태 확인
gh run list --workflow=release.yml

# 릴리스 결과 확인
gh release view v0.1.0
```

- https://github.com/itdar/shield-agent/actions 에서 release workflow 성공 여부 확인
- 실패 시 로그 확인 후 수정, 태그 삭제 후 재생성:
  ```bash
  git tag -d v0.1.0
  git push origin :refs/tags/v0.1.0
  # 수정 후
  git tag v0.1.0
  git push origin v0.1.0
  ```

## 3. Homebrew tap 저장소 생성

```bash
# 저장소 생성
gh repo create itdar/homebrew-tap --public --description "Homebrew formulae for itdar projects"

# 클론 후 Formula 복사
git clone https://github.com/itdar/homebrew-tap.git /tmp/homebrew-tap
cp Formula/shield-agent.rb /tmp/homebrew-tap/shield-agent.rb

# sha256 업데이트 (릴리스 완료 후)
# checksums.txt에서 각 플랫폼 sha256 확인:
gh release download v0.1.0 --pattern "checksums.txt" --dir /tmp
cat /tmp/checksums.txt
# Formula 파일의 # sha256 "REPLACE_WITH_ACTUAL_SHA256" 부분을 실제 값으로 교체

# 커밋 & 푸시
cd /tmp/homebrew-tap
git add shield-agent.rb
git commit -m "Add shield-agent 0.1.0"
git push

# 테스트
brew tap itdar/tap
brew install shield-agent
```

## 4. curl 설치 테스트

```bash
# 릴리스 완료 후 테스트
curl -sSL https://raw.githubusercontent.com/itdar/shield-agent/main/scripts/install.sh | sh

# 설치 확인
shield-agent --help
```

## 5. Docker 이미지 테스트

```bash
docker pull ghcr.io/itdar/shield-agent:0.1.0
docker run --rm ghcr.io/itdar/shield-agent:0.1.0 --help
```

## 6. ROADMAP Phase 2 잔여 — 문서

- [ ] README.md 영문 버전 (오픈소스 메인)
- [ ] CONTRIBUTING.md 작성
- [ ] CODE_OF_CONDUCT.md 작성
- [ ] docs/ 내 한글 문서에 mermaid 아키텍처 다이어그램 추가

## 7. 실제 MCP 서버로 통합 테스트

- [ ] stdio 모드로 실제 MCP 서버 래핑 테스트
- [ ] proxy 모드로 실제 MCP 서버 프록시 테스트
- [ ] 각 미들웨어 동작 확인 (auth, guard, log)
- [ ] SIGHUP 리로드 확인
- [ ] `docs/testing-guide.md` 참고
