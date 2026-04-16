#!/bin/bash
# perf-test.sh - k6 성능 테스트 실행 스크립트

set -euo pipefail

SCENARIO="${1:-load}"
SERVER_URL="${SERVER_URL:-http://localhost:8080}"
RESULTS_DIR="k6/results/$(date '+%Y%m%d_%H%M%S')_${SCENARIO}"

# 색상 코드
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

# ───────────────────────────────────────────────
# 사전 체크
# ───────────────────────────────────────────────
if ! command -v k6 &>/dev/null; then
  echo -e "${RED}❌ k6 미설치${NC}"
  echo "   macOS: brew install k6"
  echo "   공식: https://k6.io/docs/getting-started/installation/"
  exit 1
fi

if ! curl -sf "$SERVER_URL/health" &>/dev/null; then
  echo -e "${RED}❌ 서버 응답 없음: $SERVER_URL${NC}"
  echo "   서버 실행 후 재시도: go run ./cmd/server &"
  exit 1
fi

mkdir -p "$RESULTS_DIR"

echo ""
echo "🔥 k6 성능 테스트 시작"
echo "   시나리오: $SCENARIO"
echo "   대상 서버: $SERVER_URL"
echo "   결과 저장: $RESULTS_DIR"
echo ""

# ───────────────────────────────────────────────
# 시나리오별 k6 실행
# ───────────────────────────────────────────────

K6_SCRIPT="k6/scenarios/${SCENARIO}.js"

if [ ! -f "$K6_SCRIPT" ]; then
  echo -e "${RED}❌ 시나리오 파일 없음: $K6_SCRIPT${NC}"
  echo "   사용 가능: smoke | load | stress | spike | soak"
  exit 1
fi

# 성능 목표 임계값 (SLO)
THRESHOLDS='{"http_req_duration":["p(50)<5","p(99)<50"],"http_req_failed":["rate<0.001"]}'

k6 run \
  --out "json=${RESULTS_DIR}/raw.json" \
  --summary-export "${RESULTS_DIR}/summary.json" \
  -e "BASE_URL=$SERVER_URL" \
  "$K6_SCRIPT" \
  2>&1 | tee "${RESULTS_DIR}/output.log"

EXIT_CODE=$?

# ───────────────────────────────────────────────
# 결과 분석 및 출력
# ───────────────────────────────────────────────
echo ""
echo "══════════════════════════════════════════"
echo "  📊 성능 테스트 결과: $SCENARIO"
echo "══════════════════════════════════════════"

if [ -f "${RESULTS_DIR}/summary.json" ]; then
  # jq로 주요 지표 추출 (jq 없으면 스킵)
  if command -v jq &>/dev/null; then
    P50=$(jq -r '.metrics.http_req_duration.values["p(50)"] // "N/A"' "${RESULTS_DIR}/summary.json" 2>/dev/null || echo "N/A")
    P99=$(jq -r '.metrics.http_req_duration.values["p(99)"] // "N/A"' "${RESULTS_DIR}/summary.json" 2>/dev/null || echo "N/A")
    RPS=$(jq -r '.metrics.http_reqs.values.rate // "N/A"' "${RESULTS_DIR}/summary.json" 2>/dev/null || echo "N/A")
    ERR=$(jq -r '.metrics.http_req_failed.values.rate // "N/A"' "${RESULTS_DIR}/summary.json" 2>/dev/null || echo "N/A")
    
    echo ""
    echo "  지표               측정값      목표"
    echo "  ─────────────────────────────────────"
    printf "  P50 Latency      %7.1fms    < 5ms\n" "$P50" 2>/dev/null || echo "  P50 Latency      N/A"
    printf "  P99 Latency      %7.1fms    < 50ms\n" "$P99" 2>/dev/null || echo "  P99 Latency      N/A"
    printf "  Throughput       %7.1f RPS  > 1,000 RPS\n" "$RPS" 2>/dev/null || echo "  Throughput       N/A"
    printf "  Error Rate       %7.3f%%    < 0.1%%\n" "$(echo "$ERR * 100" | bc -l 2>/dev/null || echo 0)" 2>/dev/null || echo "  Error Rate       N/A"
    echo ""
  fi
fi

if [ $EXIT_CODE -eq 0 ]; then
  echo -e "  ${GREEN}✅ 모든 임계값(SLO) 통과${NC}"
else
  echo -e "  ${RED}❌ 일부 임계값(SLO) 실패 - ${RESULTS_DIR}/output.log 확인${NC}"
fi

echo ""
echo "  📁 결과 저장 위치: $RESULTS_DIR"
echo "══════════════════════════════════════════"

exit $EXIT_CODE
