package server

import "errors"

// 공개 sentinel 에러. 호출부는 errors.Is 로 판별해 exit code 매핑 및
// 제어 흐름을 결정한다. 각 에러의 발생 조건은 FEATURE_SPEC.md FR-002 ~ FR-010 참조.
//
// Run 반환 카테고리 (FR-010 단일 소스):
//   - ErrServeFailed:     리스너/Accept 등 Serve 조기 실패를 %w 래핑하여 반환
//   - ErrShutdownTimeout: graceful shutdown 데드라인 초과 (context.DeadlineExceeded 를 %w 래핑)
//   - ErrAlreadyRunning:  Run 을 중복 호출한 경우 (프로그래밍 오류)
//
// Handle 사전 검증 카테고리 (panic-free 계약):
//   - ErrCannotRegisterAfterRun: Run 이 이미 호출된 뒤 Handle 시도
//   - ErrInvalidHandler:         h == nil
//   - ErrInvalidPattern:         pattern 이 비었거나 http.ServeMux 허용 형식 위반
//   - ErrDuplicatePattern:       이미 등록된 pattern 재등록 (mux.Handle 은 호출되지 않음)
var (
	ErrServeFailed     = errors.New("server: serve failed")
	ErrShutdownTimeout = errors.New("server: shutdown deadline exceeded")
	ErrAlreadyRunning  = errors.New("server: already running")

	ErrCannotRegisterAfterRun = errors.New("server: cannot register after run")
	ErrDuplicatePattern       = errors.New("server: duplicate pattern")
	ErrInvalidHandler         = errors.New("server: invalid handler")
	ErrInvalidPattern         = errors.New("server: invalid pattern")
)
