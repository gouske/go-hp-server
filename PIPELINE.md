# Go 고성능 서버 개발 파이프라인

## 개요

Claude Code(개발자) ↔ Codex CLI(리뷰어) 듀얼 AI 파이프라인으로  
실무 수준의 Go 고성능 서버를 체계적으로 구축한다.

```
┌─────────────────────────────────────────────────────────────┐
│                     개발 파이프라인                             │
│                                                             │
│  [기능 명세 작성]  →  [명세 리뷰]    →    [구현]   →   [코드 리뷰]   │
│   Claude Code        Codex       Claude Code      Codex     │
│                        ↓                            ↓       │
│                    [명세 수정]                    [코드 수정]   │
│                   Claude Code                  Claude Code  │
│                        ↓                            ↓       │
│                   [재리뷰 → 승인]                [재리뷰 → 승인] │
│                                                     ↓       │
│                                               [성능 테스트 k6] │
│                                                     ↓       │
│                                                 [다음 기능]   │
└─────────────────────────────────────────────────────────────┘
```

---

## 역할 분담

| 역할 | 도구 | 책임 |
|------|------|------|
| **메인 개발자** | Claude Code | 기능 명세 작성, 구현, 수정 |
| **리뷰어** | Codex CLI | 명세 검토, 코드 리뷰, 취약점 탐지 |
| **성능 테스터** | k6 | 부하 테스트, 성능 지표 수집 |

---

## 파이프라인 단계

### Phase 0: 프로젝트 초기화 (1회)

```bash
./scripts/init.sh
```

- Go 모듈 초기화
- 디렉토리 구조 생성
- Git 저장소 초기화
- k6, 의존성 도구 설치 확인

---

### Phase 1: 기능 명세 작성 + 리뷰

#### Step 1-A: Claude Code가 명세 작성

```bash
# Claude Code에게 지시
claude "AGENTS.md를 읽고 docs/FEATURE_SPEC.md를 작성해줘. 
우선순위 1번 기능부터 시작해."
```

산출물: `docs/FEATURE_SPEC.md`

#### Step 1-B: Codex가 명세 리뷰

```bash
./scripts/spec-review.sh
```

산출물: `docs/SPEC_REVIEW.md`

#### Step 1-C: 명세 승인 여부 판단

- ✅ **APPROVED** → Phase 2 진행
- ❌ **CHANGES_NEEDED** → Claude Code가 수정 후 재리뷰

---

### Phase 2: 구현 + 코드 리뷰

#### Step 2-A: Claude Code가 구현

```bash
claude "docs/FEATURE_SPEC.md의 [기능명]을 구현해줘. 
AGENTS.md의 코딩 표준을 따르고, 구현 완료 후 
docs/IMPL_SUMMARY.md에 변경사항을 정리해줘."
```

#### Step 2-B: Codex가 코드 리뷰

```bash
./scripts/code-review.sh [feature-name]
```

산출물: `docs/reviews/CODE_REVIEW_[feature].md`

#### Step 2-C: 수정 루프

```bash
# 이슈가 있으면
claude "docs/reviews/CODE_REVIEW_[feature].md의 이슈를 수정해줘."

# 재리뷰
./scripts/code-review.sh [feature-name] --re-review
```

---

### Phase 3: 성능 테스트

```bash
# 서버 실행
go run ./cmd/server &

# k6 부하 테스트
./scripts/perf-test.sh [scenario]
```

시나리오:
- `smoke` - 최소 부하, 기본 동작 확인 (1 VU, 1분)
- `load` - 정상 부하 (50 VU, 5분)
- `stress` - 한계 부하 (200 VU, 10분)
- `spike` - 급격한 트래픽 폭증
- `soak` - 장시간 안정성 (30 VU, 30분)

---

## 기능 우선순위 (초기 설정)

Claude Code가 `docs/FEATURE_SPEC.md`를 통해 구체화한 뒤 Codex와 합의.

### P0 - 코어 인프라 (서버가 동작하기 위한 필수 요소)

1. **프로젝트 골격** - 디렉토리 구조, Go 모듈, 설정 로더
2. **HTTP/TCP 서버 기반** - net/http 또는 fasthttp, graceful shutdown
3. **구조화 로깅** - zerolog, 요청 ID 트레이싱
4. **설정 관리** - Viper, 환경변수, YAML 지원
5. **헬스체크** - `/health`, `/ready` 엔드포인트

### P1 - 성능 핵심

6. **Worker Pool** - 고루틴 풀, 작업 큐
7. **Connection Pool** - DB/Redis 연결 풀
8. **Rate Limiter** - 토큰 버킷, IP/API키 기반
9. **Circuit Breaker** - 외부 서비스 장애 격리

### P2 - 운영 필수

10. **Prometheus 메트릭** - RED 메트릭 (Rate, Error, Duration)
11. **미들웨어 체인** - Auth, CORS, Request ID, Recovery
12. **Graceful Degradation** - 부분 장애 시 서비스 유지

### P3 - 고급 기능

13. **캐싱 레이어** - Redis 캐시, In-memory LRU
14. **gRPC 지원** - protobuf, bidirectional streaming
15. **배포 자동화** - Dockerfile, docker-compose, Makefile

---

## 디렉토리 구조

```
go-highperf-server/
├── AGENTS.md                  # Claude Code 지시사항
├── CODEX.md                   # Codex 리뷰 지시사항
├── PIPELINE.md                # 이 문서
├── Makefile
├── go.mod
├── go.sum
│
├── cmd/
│   └── server/
│       └── main.go
│
├── internal/
│   ├── config/               # 설정 관리
│   ├── server/               # HTTP/TCP 서버
│   ├── middleware/           # 미들웨어
│   ├── handler/              # 요청 핸들러
│   ├── worker/               # Worker Pool
│   ├── ratelimit/            # Rate Limiter
│   ├── circuitbreaker/       # Circuit Breaker
│   ├── metrics/              # Prometheus 메트릭
│   └── logger/               # 구조화 로깅
│
├── pkg/                      # 외부 공개 가능한 패키지
│
├── docs/
│   ├── FEATURE_SPEC.md       # 기능 명세서 (Claude Code 작성)
│   ├── SPEC_REVIEW.md        # 명세 리뷰 (Codex 작성)
│   ├── IMPL_SUMMARY.md       # 구현 요약
│   └── reviews/              # 코드 리뷰 히스토리
│
├── scripts/
│   ├── init.sh               # 초기화
│   ├── spec-review.sh        # 명세 리뷰 실행
│   ├── code-review.sh        # 코드 리뷰 실행
│   └── perf-test.sh          # 성능 테스트 실행
│
└── k6/
    ├── scenarios/
    │   ├── smoke.js
    │   ├── load.js
    │   ├── stress.js
    │   ├── spike.js
    │   └── soak.js
    └── helpers/
        └── common.js
```

---

## 리뷰 품질 기준

### Codex 코드 리뷰 체크리스트

| 카테고리 | 항목 |
|----------|------|
| **정확성** | 로직 버그, 경쟁 조건(race condition), 데드락 |
| **성능** | 불필요한 할당, goroutine 누수, 블로킹 호출 |
| **보안** | 입력 검증, SQL 인젝션, 시크릿 노출 |
| **에러 처리** | 미처리 에러, 패닉 복구, 타임아웃 |
| **테스트 가능성** | 인터페이스 분리, 목(mock) 가능성 |
| **Go 관용어** | 패키지 구조, 네이밍, 에러 래핑 |

### 판정 기준

- **APPROVED**: 이슈 없음 → 다음 단계 진행
- **CHANGES_NEEDED**: 수정 필요 항목 목록화 → Claude Code 수정
- **REJECTED**: 설계 문제 → 명세부터 재검토

---

## 성능 목표

| 지표 | 목표 |
|------|------|
| Throughput | > 10,000 RPS (단일 서버) |
| P50 Latency | < 5ms |
| P99 Latency | < 50ms |
| Error Rate | < 0.1% |
| Memory | < 256MB (50 VU 기준) |
| Goroutine Leak | 0 |

---

## 커밋 컨벤션

```
feat(worker-pool): Worker Pool 구현 - P1-6
fix(rate-limiter): 토큰 버킷 경쟁 조건 수정 - Codex 리뷰 반영
perf(handler): 불필요한 메모리 할당 제거
test(circuit-breaker): 장애 복구 테스트 추가
```

태그 형식: `feat|fix|perf|test|docs(scope): 설명 - 출처`
