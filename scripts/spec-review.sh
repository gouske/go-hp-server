#!/bin/bash
# spec-review.sh - Codex CLI로 기능 명세 리뷰 실행

set -euo pipefail

SPEC_FILE="docs/FEATURE_SPEC.md"
REVIEW_FILE="docs/SPEC_REVIEW.md"
TIMESTAMP=$(date '+%Y-%m-%d %H:%M')

# ───────────────────────────────────────────────
# 사전 체크
# ───────────────────────────────────────────────
if [ ! -f "$SPEC_FILE" ]; then
  echo "❌ $SPEC_FILE 없음. Claude Code로 먼저 명세를 작성하세요."
  echo ""
  echo "  claude 'AGENTS.md를 읽고 docs/FEATURE_SPEC.md를 작성해줘'"
  exit 1
fi

if ! command -v codex &>/dev/null; then
  echo "❌ codex CLI가 설치되어 있지 않습니다."
  echo "   npm install -g @openai/codex"
  exit 1
fi

echo "📋 기능 명세 리뷰 시작..."
echo "   대상: $SPEC_FILE"
echo "   출력: $REVIEW_FILE"
echo ""

# ───────────────────────────────────────────────
# Codex에게 리뷰 지시
# ───────────────────────────────────────────────
PROMPT=$(cat << PROMPT_EOF
CODEX.md와 AGENTS.md를 읽어라.

아래는 Claude Code가 작성한 기능 명세서다:

$(cat "$SPEC_FILE")

CODEX.md의 '명세 리뷰 지시사항'에 따라 이 명세를 검토하고,
'명세 리뷰 출력 형식'에 맞춰 $REVIEW_FILE 파일을 작성해라.

판정은 APPROVED, CHANGES_NEEDED, REJECTED 중 하나여야 한다.
파일 작성 후 판정 결과를 마지막 줄에 출력해라: VERDICT:[판정]
PROMPT_EOF
)

echo "🤖 Codex 실행 중..."
RESULT=$(codex "$PROMPT" 2>&1)

echo "$RESULT"

# ───────────────────────────────────────────────
# 판정 추출
# ───────────────────────────────────────────────
VERDICT=$(echo "$RESULT" | grep -o 'VERDICT:[A-Z_]*' | head -1 | cut -d: -f2 || echo "UNKNOWN")

echo ""
echo "══════════════════════════════════════════"

case "$VERDICT" in
  APPROVED)
    echo "  ✅ 명세 리뷰: APPROVED"
    echo ""
    echo "  다음 단계:"
    echo "  claude '$(grep -m1 '^## ' "$SPEC_FILE" | sed 's/## //')을 구현해줘. AGENTS.md의 코딩 표준을 따라.'"
    ;;
  CHANGES_NEEDED)
    echo "  🟡 명세 리뷰: CHANGES_NEEDED"
    echo ""
    echo "  다음 단계:"
    echo "  1. $REVIEW_FILE 확인"
    echo "  2. claude '$REVIEW_FILE의 CRITICAL 이슈를 반영해서 FEATURE_SPEC.md를 수정해줘'"
    echo "  3. ./scripts/spec-review.sh (재리뷰)"
    ;;
  REJECTED)
    echo "  🔴 명세 리뷰: REJECTED"
    echo ""
    echo "  다음 단계:"
    echo "  1. $REVIEW_FILE 확인"
    echo "  2. claude '$REVIEW_FILE를 읽고 FEATURE_SPEC.md를 처음부터 다시 작성해줘'"
    ;;
  *)
    echo "  ⚠️  판정 불명확 - $REVIEW_FILE를 직접 확인하세요"
    ;;
esac

echo "══════════════════════════════════════════"

# 리뷰 이력 로그
mkdir -p docs/review-history
cp "$REVIEW_FILE" "docs/review-history/SPEC_REVIEW_$(date '+%Y%m%d_%H%M%S').md" 2>/dev/null || true
