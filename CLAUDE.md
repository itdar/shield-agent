# CLAUDE.md - Project Configuration for Claude Code

## Git Workflow Rules

### Commit Convention
- Format: `type(scope): description`
- Types: feat, fix, docs, refactor, test, chore, style, perf
- Scope: 모듈 또는 컴포넌트 이름 (선택)
- Description: 명령형, 최대 72자, 한국어 OK
- 예시: `feat(auth): 로그인 JWT 토큰 인증 구현`

### Branch Naming
- Feature: `feat/간단한-설명` or `feat/TICKET-123-설명`
- Bugfix: `fix/간단한-설명`
- Hotfix: `hotfix/간단한-설명`
- Chore: `chore/간단한-설명`

### PR Rules
- PR 제목은 커밋 컨벤션과 동일한 포맷
- 변경사항 요약을 bullet point로 포함
- 관련 이슈가 있으면 링크

### Auto Commit Behavior
- 작업 완료 후 커밋을 요청받으면:
  1. `git status`로 변경사항 확인
  2. 관련 파일을 논리적 단위로 그룹핑
  3. Conventional Commit 형식으로 커밋 메시지 작성
  4. 커밋 실행
- 큰 변경은 여러 커밋으로 분리 (논리적 단위별)
- 커밋 메시지 본문에 "왜" 변경했는지 포함

### Push Rules
- push 전 현재 브랜치 확인
- main/master 직접 push 금지 → 브랜치 생성 후 PR
- force push 절대 금지

## Code Style
- 이 프로젝트의 기존 코드 스타일을 따를 것
- 린터/포매터 설정이 있으면 커밋 전 실행

## Safety
- `.env`, secrets, API key 등 민감 정보 절대 커밋 금지
- `git reset --hard`, `git clean -fd` 같은 파괴적 명령은 반드시 확인 후 실행
- 대규모 변경 전 체크포인트(커밋) 생성