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
