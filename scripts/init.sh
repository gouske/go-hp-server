#!/bin/bash
# init.sh - Go 고성능 서버 프로젝트 초기화

set -euo pipefail

PROJECT_NAME="${1:-go-highperf-server}"
MODULE_NAME="${2:-github.com/$(whoami)/$PROJECT_NAME}"

echo "🚀 프로젝트 초기화: $PROJECT_NAME"
echo "📦 모듈명: $MODULE_NAME"
echo ""

# ───────────────────────────────────────────────
# 1. 디렉토리 구조 생성
# ───────────────────────────────────────────────
echo "📁 디렉토리 구조 생성..."

mkdir -p "$PROJECT_NAME"/{cmd/server,internal/{config,server,middleware,handler,worker,ratelimit,circuitbreaker,metrics,logger},pkg,docs/reviews,scripts,k6/{scenarios,helpers}}

cd "$PROJECT_NAME"

# ───────────────────────────────────────────────
# 2. Go 모듈 초기화
# ───────────────────────────────────────────────
echo "🔧 Go 모듈 초기화..."
go mod init "$MODULE_NAME"

# ───────────────────────────────────────────────
# 3. 기본 파일 생성
# ───────────────────────────────────────────────
echo "📝 기본 파일 생성..."

# main.go 스켈레톤
cat > cmd/server/main.go << 'EOF'
// main은 고성능 서버의 진입점이다.
// 설정 로드 → 서버 초기화 → Graceful Shutdown 순으로 동작한다.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// TODO: 서버 초기화 및 실행
	<-ctx.Done()
}
EOF

# .gitignore
cat > .gitignore << 'EOF'
# 바이너리
/bin/
*.exe

# 설정 파일 (시크릿 포함 가능)
config/local.yaml
*.env
.env.*

# 테스트 결과
coverage.out
*.test

# k6 결과
k6/results/

# IDE
.idea/
.vscode/
*.swp
EOF

# Makefile
cat > Makefile << 'EOF'
.PHONY: build run test lint vet clean perf-smoke perf-load perf-stress

BINARY=bin/server
CMD=./cmd/server

build:
	go build -o $(BINARY) $(CMD)

run:
	go run $(CMD)

test:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

vet:
	go vet ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/ coverage.out

# 성능 테스트 (서버가 실행 중이어야 함)
perf-smoke:
	./scripts/perf-test.sh smoke

perf-load:
	./scripts/perf-test.sh load

perf-stress:
	./scripts/perf-test.sh stress

perf-soak:
	./scripts/perf-test.sh soak

# 리뷰 워크플로우
spec-review:
	./scripts/spec-review.sh

code-review:
	./scripts/code-review.sh $(FEATURE)

# 전체 파이프라인 (명세 리뷰 → 구현 대기 상태)
pipeline-start:
	@echo "📋 FEATURE_SPEC.md 확인 후 Claude Code에게 구현 지시"
	@cat docs/FEATURE_SPEC.md 2>/dev/null || echo "docs/FEATURE_SPEC.md 없음 - Claude Code로 먼저 명세 작성 필요"
EOF

# ───────────────────────────────────────────────
# 4. config.yaml 기본 설정
# ───────────────────────────────────────────────
mkdir -p config
cat > config/default.yaml << 'EOF'
server:
  host: "0.0.0.0"
  port: 8080
  read_timeout: 30s
  write_timeout: 30s
  idle_timeout: 120s
  graceful_shutdown_timeout: 30s

worker_pool:
  size: 100
  queue_size: 10000

rate_limiter:
  enabled: true
  requests_per_second: 1000
  burst: 2000

circuit_breaker:
  enabled: true
  max_requests: 100
  interval: 60s
  timeout: 30s

metrics:
  enabled: true
  path: "/metrics"

log:
  level: "info"
  format: "json"
EOF

echo "✅ 기본 파일 생성 완료"

# ───────────────────────────────────────────────
# 5. docs 초기화
# ───────────────────────────────────────────────
echo "📚 문서 초기화..."

cat > docs/IMPL_SUMMARY.md << 'EOF'
# 구현 요약

| 기능 | 우선순위 | 상태 | 리뷰 | 완료일 |
|------|----------|------|------|--------|
| 프로젝트 골격 | P0-1 | 🔄 진행 중 | - | - |

## 변경 이력

### YYYY-MM-DD - 프로젝트 초기화
- 디렉토리 구조 생성
- Go 모듈 초기화
- 기본 설정 파일 생성
EOF

echo "✅ docs 초기화 완료"

# ───────────────────────────────────────────────
# 6. Git 초기화
# ───────────────────────────────────────────────
echo "🗂️ Git 초기화..."
git init
git add .
git commit -m "chore: 프로젝트 초기 구조 생성"
echo "✅ Git 초기화 완료"

# ───────────────────────────────────────────────
# 7. 도구 확인
# ───────────────────────────────────────────────
echo ""
echo "🔍 필수 도구 확인..."

check_tool() {
  if command -v "$1" &>/dev/null; then
    echo "  ✅ $1 설치됨"
  else
    echo "  ❌ $1 미설치 - $2"
  fi
}

check_tool "go" "https://go.dev/doc/install"
check_tool "k6" "brew install k6"
check_tool "golangci-lint" "brew install golangci-lint"
check_tool "codex" "npm install -g @openai/codex"

echo ""
echo "══════════════════════════════════════════"
echo "  🎉 초기화 완료!"
echo ""
echo "  다음 단계:"
echo "  1. claude 'AGENTS.md를 읽고 docs/FEATURE_SPEC.md를 작성해줘'"
echo "  2. make spec-review"
echo "  3. claude 'P0-1 프로젝트 골격을 구현해줘'"
echo "  4. make code-review FEATURE=skeleton"
echo "══════════════════════════════════════════"
