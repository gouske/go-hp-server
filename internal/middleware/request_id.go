package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/rs/zerolog"
)

// HeaderRequestID 는 요청/응답에서 사용되는 요청 ID 헤더 이름이다.
const HeaderRequestID = "X-Request-ID"

// IDSource 는 요청 ID 의 출처를 나타낸다 (클라이언트 제공 vs 서버 생성).
type IDSource string

// IDSource 값. 로그 필드 `request_id_source` 에 그대로 기록된다.
const (
	IDSourceClient    IDSource = "client"
	IDSourceGenerated IDSource = "generated"
)

// IDGenerator 는 요청 ID 를 반환하는 함수 시그니처이다.
// 실패 시 에러를 반환해야 하며, 호출부(RequestID 미들웨어) 는 500 응답으로 전환한다.
type IDGenerator func() (string, error)

// clientIDPattern 은 클라이언트 제공 X-Request-ID 의 허용 형식이다.
// 8~128 자, 영숫자/점/언더스코어/하이픈만 허용 (FR-021).
var clientIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{8,128}$`)

// requestIDConfig 는 RequestID 미들웨어 내부 설정이다.
type requestIDConfig struct {
	generator   IDGenerator
	errorLogger *zerolog.Logger
}

// RequestIDOption 은 NewRequestID 의 함수형 옵션 타입이다.
// 옵션은 설정 중 유효성 검증에서 실패할 수 있으므로 error 를 반환한다 (REV5-002).
type RequestIDOption func(*requestIDConfig) error

// WithIDGenerator 는 기본 crypto/rand 기반 생성기를 임의 생성기로 교체한다.
// 주로 테스트(결정적 ID, 실패 경로 주입) 에서 사용한다.
// gen == nil 이면 NewRequestID 가 (nil, error) 를 반환한다.
func WithIDGenerator(gen IDGenerator) RequestIDOption {
	return func(c *requestIDConfig) error {
		if gen == nil {
			return errors.New("middleware: id generator must not be nil")
		}
		c.generator = gen
		return nil
	}
}

// WithErrorLogger 는 ID 생성 실패 시 에러 로그를 출력할 로거를 주입한다.
// l == nil 이면 NewRequestID 가 (nil, error) 를 반환한다.
// 미지정이면 zerolog.Nop() 를 기본값으로 사용한다.
func WithErrorLogger(l *zerolog.Logger) RequestIDOption {
	return func(c *requestIDConfig) error {
		if l == nil {
			return errors.New("middleware: error logger must not be nil")
		}
		c.errorLogger = l
		return nil
	}
}

// defaultIDGenerator 는 crypto/rand 로 16 byte 를 읽어 hex 인코딩한 32자 ID 를 반환한다.
func defaultIDGenerator() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("crypto/rand read: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// requestIDCtxKey 는 ctx 에 요청 ID/출처를 보관할 때 사용하는 비공개 키 타입이다.
type requestIDCtxKey struct{}

// requestIDValue 는 ctx 에 보관되는 요청 ID 정보이다.
type requestIDValue struct {
	id     string
	source IDSource
}

// NewRequestID 는 요청마다 고유 ID 를 생성/검증해 ctx 및 응답 헤더에 주입하는 미들웨어 팩토리를 반환한다.
// 옵션 검증 실패 시 (nil, error) 를 반환하며 panic 하지 않는다 (NFR-025).
//
// 동작 (FR-020 ~ FR-022, FR-030):
//   - 클라이언트가 X-Request-ID 헤더를 제공하고 값이 clientIDPattern 을 만족하면 그대로 사용 (출처=client).
//   - 다중 X-Request-ID 헤더가 전달되면 첫 번째 값만 평가한다 (REV6-003).
//   - 아니면 ID 생성기를 호출해 새 ID 를 생성한다 (출처=generated).
//   - 생성 실패 시 500 응답 + 에러 로그. next.ServeHTTP 는 호출하지 않는다.
//   - 응답 헤더 X-Request-ID 로 항상 ID 를 반환한다.
func NewRequestID(opts ...RequestIDOption) (func(http.Handler) http.Handler, error) {
	cfg := &requestIDConfig{
		generator: defaultIDGenerator,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}
	if cfg.errorLogger == nil {
		nop := zerolog.Nop()
		cfg.errorLogger = &nop
	}

	return func(next http.Handler) http.Handler {
		// CR-001: next == nil 입력에 대한 panic-free 방어.
		if next == nil {
			next = http.NotFoundHandler()
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, source, ok := acceptClientID(r)
			if !ok {
				generated, err := cfg.generator()
				if err != nil {
					wrapped := fmt.Errorf("middleware request_id: %w", err)
					cfg.errorLogger.Error().Err(wrapped).Msg("request id generation failed")
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
					return
				}
				id = generated
				source = IDSourceGenerated
			}

			w.Header().Set(HeaderRequestID, id)
			ctx := context.WithValue(r.Context(), requestIDCtxKey{}, requestIDValue{id: id, source: source})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}, nil
}

// acceptClientID 는 요청 헤더에서 클라이언트 제공 ID 를 평가한다.
// 다중 헤더가 있으면 첫 번째 값만 대상으로 하며, 양끝 공백을 제거한 후 정규식 검증한다 (REV6-003).
// 수용되면 (id, IDSourceClient, true), 아니면 ("", "", false) 를 반환한다.
func acceptClientID(r *http.Request) (string, IDSource, bool) {
	values := r.Header.Values(HeaderRequestID)
	if len(values) == 0 {
		return "", "", false
	}
	candidate := strings.TrimSpace(values[0])
	if candidate == "" {
		return "", "", false
	}
	if !clientIDPattern.MatchString(candidate) {
		return "", "", false
	}
	return candidate, IDSourceClient, true
}

// IDFromContext 는 RequestID 미들웨어가 ctx 에 심은 요청 ID 를 반환한다.
// 바인딩이 없으면 빈 문자열을 반환한다.
func IDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, ok := ctx.Value(requestIDCtxKey{}).(requestIDValue)
	if !ok {
		return ""
	}
	return v.id
}

// SourceFromContext 는 요청 ID 의 출처를 반환한다.
// 바인딩이 없으면 빈 IDSource 를 반환한다.
func SourceFromContext(ctx context.Context) IDSource {
	if ctx == nil {
		return ""
	}
	v, ok := ctx.Value(requestIDCtxKey{}).(requestIDValue)
	if !ok {
		return ""
	}
	return v.source
}
