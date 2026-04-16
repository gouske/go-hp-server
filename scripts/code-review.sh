#!/bin/bash
# code-review.sh - Codex CLI로 구현된 코드 리뷰 실행

set -euo pipefail

FEATURE="${1:-}"
RE_REVIEW="${2:-}"
TIMESTAMP=$(date '+%Y-%m-%d')

# ───────────────────────────────────────────────
# 사전 체크
# ───────────────────────────────────────────────
if [ -z "$FEATURE" ]; then
  echo "사용법: ./scripts/code-review.sh <feature-name> [--re-review]"
  echo ""
  echo "예시:"
  echo "  ./scripts/code-review.sh worker-pool"
  echo "  ./scripts/code-review.sh worker-pool --re-review"
  exit 1
fi

REVIEW_FILE="docs/reviews/CODE_REVIEW_${FEATURE}.md"
PREV_REVIEW_FILE="${REVIEW_FILE}"

if ! command -v codex &>/dev/null; then
  echo "❌ codex CLI가 설치되어 있지 않습니다."
  echo "   npm install -g @openai/codex"
  exit 1
fi

# ───────────────────────────────────────────────
# 변경된 파일 수집
# ───────────────────────────────────────────────
echo "📂 변경된 Go 파일 수집..."

# git으로 변경된 파일 목록 (추적되지 않은 새 파일 포함)
CHANGED_FILES=$(git diff --name-only HEAD 2>/dev/null; git ls-files --others --exclude-standard 2>/dev/null | grep '\.go$') 
CHANGED_FILES=$(echo "$CHANGED_FILES" | grep '\.go$' | sort -u || true)

if [ -z "$CHANGED_FILES" ]; then
  # git이 없거나 변경사항 없으면 최근 수정된 파일 탐색
  CHANGED_FILES=$(find . -name '*.go' -newer go.mod -not -path './.git/*' 2>/dev/null | sort)
fi

if [ -z "$CHANGED_FILES" ]; then
  echo "⚠️  검토할 Go 파일을 찾지 못했습니다."
  echo "   구현 완료 후 실행하세요."
  exit 1
fi

echo "   검토 대상 파일:"
echo "$CHANGED_FILES" | while read -r f; do echo "   - $f"; done
echo ""

# ───────────────────────────────────────────────
# 파일 내용 수집
# ───────────────────────────────────────────────
CODE_CONTENT=""
while IFS= read -r file; do
  if [ -f "$file" ]; then
    CODE_CONTENT="$CODE_CONTENT

### $file
\`\`\`go
$(cat "$file")
\`\`\`"
  fi
done <<< "$CHANGED_FILES"

# ───────────────────────────────────────────────
# Codex 리뷰 실행
# ───────────────────────────────────────────────
echo "🤖 Codex 코드 리뷰 시작: $FEATURE"

if [ "$RE_REVIEW" = "--re-review" ] && [ -f "$PREV_REVIEW_FILE" ]; then
  echo "   모드: 재리뷰 (이전 이슈 해결 여부 확인)"
  
  PROMPT=$(cat << PROMPT_EOF
CODEX.md와 AGENTS.md를 읽어라.

이것은 재리뷰다. 이전 리뷰 결과:
$(cat "$PREV_REVIEW_FILE")

수정된 코드:
$CODE_CONTENT

CODEX.md의 '재리뷰 지시사항'에 따라:
1. 이전 CRITICAL/MAJOR 이슈가 해결됐는지 확인
2. 신규 이슈 발생 여부 확인
3. $REVIEW_FILE 파일을 업데이트해라

마지막 줄에 최종 판정 출력: VERDICT:[판정]
PROMPT_EOF
)
else
  echo "   모드: 최초 리뷰"
  
  PROMPT=$(cat << PROMPT_EOF
CODEX.md와 AGENTS.md를 읽어라.

아래는 Claude Code가 '${FEATURE}' 기능으로 구현한 Go 코드다:

$CODE_CONTENT

CODEX.md의 '코드 리뷰 지시사항'에 따라 다음을 수행해라:
1. 정확성, 성능, 보안, 에러 처리, 리소스 관리, Go 관용어 검토
2. '코드 리뷰 출력 형식'에 맞춰 $REVIEW_FILE 파일을 작성해라
3. 판정: APPROVED, CHANGES_NEEDED, REJECTED 중 하나

마지막 줄에 판정 출력: VERDICT:[판정]
PROMPT_EOF
)
fi

# codex 0.120+ 는 인자만 주면 인터랙티브 TUI 로 진입해 non-TTY 스크립트에서는
# 조용히 종료된다. 비대화형 실행은 반드시 `codex exec` 를 사용한다.
RESULT=$(codex exec "$PROMPT" 2>&1)
echo "$RESULT"

# ───────────────────────────────────────────────
# 판정 추출 및 안내
# ───────────────────────────────────────────────
VERDICT=$(echo "$RESULT" | grep -o 'VERDICT:[A-Z_]*' | head -1 | cut -d: -f2 || echo "UNKNOWN")

echo ""
echo "══════════════════════════════════════════"

case "$VERDICT" in
  APPROVED)
    echo "  ✅ 코드 리뷰: APPROVED - $FEATURE"
    echo ""
    echo "  다음 단계:"
    echo "  git add -A && git commit -m 'feat($FEATURE): 구현 완료'"
    echo "  다음 기능: docs/FEATURE_SPEC.md 업데이트 후 파이프라인 재시작"
    
    # IMPL_SUMMARY 업데이트 안내
    echo ""
    echo "  📝 docs/IMPL_SUMMARY.md 상태 업데이트 필요"
    ;;
  CHANGES_NEEDED)
    echo "  🟡 코드 리뷰: CHANGES_NEEDED - $FEATURE"
    echo ""
    echo "  다음 단계:"
    echo "  1. cat $REVIEW_FILE"
    echo "  2. claude '$REVIEW_FILE의 CRITICAL/MAJOR 이슈를 수정해줘'"
    echo "  3. ./scripts/code-review.sh $FEATURE --re-review"
    ;;
  REJECTED)
    echo "  🔴 코드 리뷰: REJECTED - $FEATURE"
    echo ""
    echo "  다음 단계:"
    echo "  1. cat $REVIEW_FILE"
    echo "  2. claude '$REVIEW_FILE를 읽고 $FEATURE를 재설계해서 다시 구현해줘'"
    ;;
  *)
    echo "  ⚠️  판정 불명확 - $REVIEW_FILE를 직접 확인하세요"
    ;;
esac

echo "══════════════════════════════════════════"

# 리뷰 이력 아카이브
mkdir -p "docs/review-history"
cp "$REVIEW_FILE" "docs/review-history/CODE_REVIEW_${FEATURE}_$(date '+%Y%m%d_%H%M%S').md" 2>/dev/null || true

# race condition 감지
echo ""
echo "🔬 Race Condition 검사 중..."
if go test -race ./... 2>&1 | grep -q "DATA RACE"; then
  echo "  ⚠️  Race condition 발견! go test -race 결과를 확인하세요"
else
  echo "  ✅ Race condition 없음"
fi
