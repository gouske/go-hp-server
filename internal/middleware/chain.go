// Package middleware 는 HTTP 요청 경계에서 공통으로 적용하는 미들웨어 모음이다.
// 본 패키지는 다음을 제공한다:
//
//   - Chain: 주어진 순서대로 http.Handler 데코레이터를 결합하는 유틸.
//   - NewRequestID: 요청마다 고유 ID 를 생성/검증해 ctx 및 응답 헤더에 주입하는 미들웨어 팩토리.
//   - NewAccessLog: 요청 완료 시점에 고정 스키마로 구조화 로그를 1회 출력하는 미들웨어 팩토리.
//
// panic 을 생성하지 않으며(Panic-Free 원칙), 하위 핸들러에서 panic 이 발생하면
// recover 없이 재전파하되 AccessLog 는 defer 로 1회 출력된다 (Recovery 는 P2-11 책임).
package middleware

import "net/http"

// Chain 은 주어진 순서대로 미들웨어를 감싸 최종 http.Handler 를 반환한다.
// Chain(h, a, b) 의 실행 순서는 요청 기준 a → b → h 이다.
//
// 입력 유효성 계약 (REV6-002):
//   - h == nil 이면 http.NotFoundHandler() 로 대체한다 (panic 없음).
//   - mws 중 nil 요소는 조용히 건너뛴다 (panic 없음).
//   - mws 가 비어 있거나 유효한 미들웨어가 하나도 없으면 h 를 그대로 반환한다.
func Chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	if h == nil {
		h = http.NotFoundHandler()
	}
	// 역순으로 감싸면 요청 기준 mws[0] → mws[1] → ... → h 순서가 된다.
	out := h
	for i := len(mws) - 1; i >= 0; i-- {
		mw := mws[i]
		if mw == nil {
			continue
		}
		out = mw(out)
	}
	return out
}
