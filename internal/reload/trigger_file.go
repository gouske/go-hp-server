package reload

import (
	"fmt"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// startFileTrigger 는 configPath 의 부모 디렉터리를 fsnotify 로 감시하고 파일명 필터로
// 해당 설정 파일의 변경 이벤트만 enqueue 한다(NFR-066).
//
// 부모 디렉터리를 감시하므로 에디터의 atomic-save(WRITE→RENAME→CREATE) 연쇄와 삭제 후
// 재생성(CREATE) 모두 동일 파일명 필터로 흡수되며, 연쇄 이벤트의 다중 enqueue 는 worker
// 의 debounce 가 1회로 병합한다. watcher 생성/등록 실패 시 에러를 반환한다(panic 없음).
func (r *Reloader) startFileTrigger() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("new watcher: %w", err)
	}
	dir := filepath.Dir(r.configPath)
	base := filepath.Base(r.configPath)
	if err := watcher.Add(dir); err != nil {
		if cerr := watcher.Close(); cerr != nil {
			return fmt.Errorf("watch dir %q: %w (close: %v)", dir, err, cerr)
		}
		return fmt.Errorf("watch dir %q: %w", dir, err)
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			if cerr := watcher.Close(); cerr != nil {
				r.logger.Warn().Err(cerr).Msg("config reload file watcher close failed")
			}
		}()
		for {
			select {
			case <-r.stopCh:
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Base(event.Name) != base {
					continue
				}
				r.enqueue(sourceFile)
			case werr, ok := <-watcher.Errors:
				if !ok {
					return
				}
				r.logger.Warn().Err(werr).Msg("config reload file watch error")
			}
		}
	}()
	return nil
}
