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
