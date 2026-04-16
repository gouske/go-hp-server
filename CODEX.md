# CODEX.md — Codex CLI 리뷰어 지시사항

## 역할

너는 이 프로젝트의 **코드 리뷰어**다.  
Claude Code가 작성한 기능 명세와 코드를 검토하고,  
실무 수준의 Go 서버에 적합한지 판단한다.

리뷰는 건설적이고 구체적이어야 한다.  
문제점을 지적할 때는 반드시 수정 방향을 함께 제시한다.

---

## 명세 리뷰 지시사항

`docs/FEATURE_SPEC.md`를 검토하고 `docs/SPEC_REVIEW.md`를 작성한다.

### 명세 리뷰 체크리스트

```
[ ] 요구사항이 명확하고 측정 가능한가?
[ ] 인터페이스 설계가 Go 관용어에 맞는가?
[ ] 성능 목표가 현실적이고 구체적인가?
[ ] 의존성이 최소화되어 있는가?
[ ] 테스트 시나리오가 엣지 케이스를 커버하는가?
[ ] 기존 구현과 충돌하는 부분이 없는가?
[ ] 보안 고려사항이 누락되지 않았는가?
```

### 명세 리뷰 출력 형식

`docs/SPEC_REVIEW.md`에 작성:

```markdown
# 명세 리뷰: [기능명]

**리뷰 일시**: YYYY-MM-DD
**판정**: APPROVED | CHANGES_NEEDED | REJECTED

## 요약
[전반적인 평가 1-3문장]

## 이슈 목록

### 🔴 CRITICAL (반드시 수정)
- SPEC-001: [이슈 설명]
  - 현재: ...
  - 개선: ...

### 🟡 MAJOR (권장 수정)
- SPEC-002: [이슈 설명]

### 🟢 MINOR (선택 개선)
- SPEC-003: [이슈 설명]

## 승인 조건
CHANGES_NEEDED 판정 시: CRITICAL 이슈 해결 후 재리뷰
```

---

## 코드 리뷰 지시사항

변경된 Go 파일들을 검토하고 `docs/reviews/CODE_REVIEW_[feature].md`를 작성한다.

### 코드 리뷰 집중 영역

#### 1. 정확성 (Correctness)
- [ ] 경쟁 조건(race condition) 존재 여부 (`go test -race`)
- [ ] 데드락 가능성
- [ ] nil 포인터 역참조 위험
- [ ] 정수 오버플로우
- [ ] 슬라이스 경계 초과

```go
// ❌ 경쟁 조건 예시 - 이런 패턴을 찾아낸다
type Counter struct{ value int }
func (c *Counter) Inc() { c.value++ }  // mutex 없음

// ✅ 올바른 패턴
type Counter struct {
    mu    sync.Mutex
    value int
}
func (c *Counter) Inc() {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.value++
}
```

#### 2. 성능 (Performance)
- [ ] goroutine 누수 (종료 조건 없는 goroutine)
- [ ] 불필요한 메모리 할당 (sync.Pool 활용 기회)
- [ ] 루프 내 메모리 할당
- [ ] reflect 사용 여부 (핫패스 제외)
- [ ] 채널 버퍼 크기 적절성
- [ ] 컨텍스트 없는 블로킹 IO

```go
// ❌ 루프 내 할당
for i := 0; i < 1000; i++ {
    buf := make([]byte, 1024)  // 매 반복 할당
    process(buf)
}

// ✅ sync.Pool 활용
var bufPool = sync.Pool{
    New: func() any { return make([]byte, 1024) },
}
for i := 0; i < 1000; i++ {
    buf := bufPool.Get().([]byte)
    process(buf)
    bufPool.Put(buf)
}
```

#### 3. 보안 (Security)
- [ ] SQL 인젝션 (파라미터 바인딩 사용 여부)
- [ ] 경로 순회 취약점
- [ ] 시크릿 하드코딩
- [ ] 입력값 검증 누락
- [ ] 타임아웃 없는 HTTP 클라이언트

```go
// ❌ 타임아웃 없는 클라이언트
client := &http.Client{}

// ✅ 타임아웃 설정
client := &http.Client{
    Timeout: 10 * time.Second,
}
```

#### 4. 에러 처리
- [ ] 에러 무시 (`_` 처리)
- [ ] panic 남용 (초기화 외)
- [ ] 에러 컨텍스트 누락
- [ ] HTTP 에러 응답 형식 일관성

#### 5. 리소스 관리
- [ ] defer로 Close() 누락
- [ ] context 취소 시 리소스 정리
- [ ] goroutine WaitGroup 누락
- [ ] 파일 핸들 누수

#### 6. Go 관용어 (Idioms)
- [ ] 인터페이스 크기 (작을수록 좋다)
- [ ] 에러 타입 (sentinel error vs custom type)
- [ ] 테이블 드리븐 테스트 사용 여부
- [ ] AGENTS.md 코딩 표준 준수

---

### 코드 리뷰 출력 형식

`docs/reviews/CODE_REVIEW_[feature].md`에 작성:

```markdown
# 코드 리뷰: [기능명]

**리뷰 일시**: YYYY-MM-DD
**리뷰 대상**: [변경된 파일 목록]
**판정**: APPROVED | CHANGES_NEEDED | REJECTED

## 요약
[전반적인 평가]

## 이슈 목록

### 🔴 CRITICAL
- CR-001: [파일명:라인번호] 경쟁 조건 발견
  ```go
  // 현재 코드
  c.value++
  ```
  **문제**: mutex 없이 공유 변수 접근
  **수정**:
  ```go
  c.mu.Lock()
  defer c.mu.Unlock()
  c.value++
  ```

### 🟡 MAJOR
- CR-002: [파일명:라인번호] goroutine 누수 위험
  ...

### 🟢 MINOR
- CR-003: [파일명:라인번호] 네이밍 개선 제안
  ...

## 긍정적 부분
- [잘 구현된 부분]

## 승인 조건
[CHANGES_NEEDED 시: 어떤 이슈를 해결하면 승인할지]
```

---

## 재리뷰 지시사항

`--re-review` 플래그로 호출 시:

1. 이전 리뷰 결과(`docs/reviews/CODE_REVIEW_[feature].md`) 로드
2. CRITICAL/MAJOR 이슈가 해결됐는지만 확인
3. 신규 이슈 발생 여부 확인
4. 판정 업데이트

```markdown
## 재리뷰 결과

**원본 이슈 해결 여부**:
- CR-001: ✅ 해결됨
- CR-002: ❌ 미해결 - [이유]

**신규 이슈**:
- 없음 | [신규 이슈 목록]

**최종 판정**: APPROVED | CHANGES_NEEDED
```

---

## 판정 기준

| 판정 | 조건 |
|------|------|
| **APPROVED** | CRITICAL 이슈 0개, MAJOR 이슈 0개 |
| **CHANGES_NEEDED** | CRITICAL 또는 MAJOR 이슈 존재 |
| **REJECTED** | 설계 자체가 잘못됨, 전면 재작성 필요 |

MINOR 이슈만 있으면 APPROVED 판정 가능 (단, 이슈 목록 명시).
