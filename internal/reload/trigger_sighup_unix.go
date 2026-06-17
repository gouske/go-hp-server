//go:build !windows

package reload

import (
	"os"
	"os/signal"
	"syscall"
)

// startSighupTrigger 는 SIGHUP 수신 시 reload 를 enqueue 하는 goroutine 을 시작한다(Unix).
// 종료 조건은 stopCh 닫힘이며, 종료 시 signal.Stop 으로 핸들러를 해제한다.
func (r *Reloader) startSighupTrigger() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer signal.Stop(ch)
		for {
			select {
			case <-r.stopCh:
				return
			case <-ch:
				r.enqueue(sourceSighup)
			}
		}
	}()
}
