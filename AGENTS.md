# AGENTS.md — Claude Code 지시사항

## 역할

너는 이 프로젝트의 **메인 개발자**다.  
Go 언어로 실무에서 사용할 수 있는 고성능 서버를 구축하는 것이 목표다.  
Codex CLI가 리뷰어로 협업하며, 리뷰 결과는 `docs/reviews/` 디렉토리에 저장된다.

---

## 절대 규칙

1. **한 번에 하나의 기능만** 구현한다. `docs/FEATURE_SPEC.md`의 우선순위 순서를 반드시 따른다.
2. **Codex 리뷰 없이 다음 기능으로 넘어가지 않는다.** 현재 기능이 APPROVED 판정을 받아야 한다.
3. **모든 공개 함수에 godoc 주석**을 작성한다.
4. **에러를 절대 무시하지 않는다.** `_`로 에러를 버리는 코드는 금지다.
5. **goroutine을 생성하면 반드시 종료 조건**을 명시한다.

---

## 작업 시작 전 체크

```bash
# 항상 최신 리뷰 결과를 확인한다
cat docs/reviews/CODE_REVIEW_*.md 2>/dev/null | tail -100

# 현재 기능 명세를 확인한다
cat docs/FEATURE_SPEC.md
```

---

## 코딩 표준

### 패키지 구조

```
internal/        - 외부 노출 금지 코드
pkg/             - 외부에서 import 가능한 코드
cmd/server/      - main 패키지만 위치
```

### 네이밍

```go
// ✅ 올바른 예
type WorkerPool struct{}
func (wp *WorkerPool) Submit(ctx context.Context, task Task) error {}

// ❌ 금지
type workerPool struct{}   // 공개 타입은 대문자
func submit(task Task) {}  // 수신자 없는 메서드
```

### 에러 처리

```go
// ✅ 에러 래핑 (컨텍스트 추가)
if err != nil {
    return fmt.Errorf("worker pool submit: %w", err)
}

// ❌ 금지 - 에러 무시
result, _ := doSomething()

// ❌ 금지 - 컨텍스트 없는 에러 반환  
return err
```

### Context 전파

```go
// ✅ 모든 IO 작업에 context 전달
func (h *Handler) GetUser(ctx context.Context, id string) (*User, error) {
    return h.db.QueryContext(ctx, query, id)
}

// ❌ context 무시
func (h *Handler) GetUser(id string) (*User, error) {}
```

### Goroutine 생명주기

```go
// ✅ 종료 조건 명시
func (wp *WorkerPool) Start(ctx context.Context) {
    for i := 0; i < wp.size; i++ {
        wp.wg.Add(1)
        go func() {
            defer wp.wg.Done()
            for {
                select {
                case task := <-wp.queue:
                    task.Execute()
                case <-ctx.Done():
                    return  // ← 종료 조건 필수
                }
            }
        }()
    }
}
```

### 구조화 로깅

```go
// ✅ zerolog 사용, 필드 추가
log.Info().
    Str("request_id", reqID).
    Str("method", r.Method).
    Str("path", r.URL.Path).
    Int("status", status).
    Dur("duration", elapsed).
    Msg("request completed")

// ❌ fmt.Println, log.Printf 사용 금지
fmt.Println("request done")
```

---

## 기능 구현 순서 (PIPELINE.md 기준)

각 기능 구현 후 아래 체크리스트를 완료해야 한다:

```markdown
## 구현 완료 체크리스트

- [ ] 기능이 FEATURE_SPEC.md의 요구사항을 충족한다
- [ ] 모든 공개 함수에 godoc 주석이 있다
- [ ] 에러 처리가 완전하다 (에러 무시 없음)
- [ ] Context가 모든 IO 작업에 전달된다
- [ ] goroutine이 있다면 종료 조건이 있다
- [ ] 단위 테스트가 작성되어 있다 (커버리지 > 70%)
- [ ] `go vet ./...` 통과
- [ ] `go build ./...` 성공
- [ ] IMPL_SUMMARY.md 업데이트 완료
```

---

## 기능 명세서 작성 형식

`docs/FEATURE_SPEC.md`에 아래 형식으로 작성한다:

```markdown
# 기능 명세서

## [기능명] - P[우선순위 번호]

### 목적
이 기능이 왜 필요한가

### 요구사항
- FR-001: 기능 요구사항 1
- FR-002: 기능 요구사항 2
- NFR-001: 비기능 요구사항 (성능 목표 등)

### 인터페이스 설계
\`\`\`go
// 공개 인터페이스 정의
type WorkerPool interface {
    Submit(ctx context.Context, task Task) error
    Stop() error
}
\`\`\`

### 패키지 위치
internal/worker/

### 의존성
- 없음 / 또는 internal/logger

### 테스트 시나리오
1. 정상 작업 제출 및 처리
2. 풀 용량 초과 시 거절
3. context 취소 시 graceful 종료

### 성능 목표
- 처리량: > 100,000 작업/초
- 지연: < 1ms P99
```

---

## Codex 리뷰 반영 방법

```bash
# 리뷰 결과 확인
cat docs/reviews/CODE_REVIEW_[feature].md

# 이슈별 수정 후 커밋
git add -p
git commit -m "fix(worker-pool): [이슈 요약] - Codex 리뷰 반영"

# 재리뷰 요청
./scripts/code-review.sh [feature] --re-review
```

---

## 금지 사항

```go
// ❌ panic 사용 금지 (main 초기화 제외)
panic("something went wrong")

// ❌ 전역 변수 사용 금지
var globalPool *WorkerPool

// ❌ init() 함수 사용 금지
func init() { ... }

// ❌ 하드코딩된 값
const timeout = 30  // 설정 파일에서 읽어야 함

// ❌ 에러 무시
http.ListenAndServe(":8080", nil)  // 반환값 무시

// ✅ 올바른 방법
if err := http.ListenAndServe(":8080", nil); err != nil {
    log.Fatal().Err(err).Msg("server failed")
}
```

---

## 의존성 허용 목록

| 패키지 | 용도 |
|--------|------|
| `github.com/rs/zerolog` | 구조화 로깅 |
| `github.com/spf13/viper` | 설정 관리 |
| `github.com/prometheus/client_golang` | 메트릭 |
| `github.com/sony/gobreaker` | Circuit Breaker |
| `golang.org/x/time/rate` | Rate Limiting |
| `github.com/stretchr/testify` | 테스트 |
| `github.com/valyala/fasthttp` | 고성능 HTTP (승인 시) |

다른 의존성 추가 시 `docs/FEATURE_SPEC.md`에 이유를 명시하고 Codex 승인을 받는다.
