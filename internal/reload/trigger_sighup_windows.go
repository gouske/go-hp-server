//go:build windows

package reload

// startSighupTrigger 는 Windows 에서 SIGHUP 이 없으므로 no-op 이며 startup 경고를 1회 남긴다.
// 파일 watch trigger 는 크로스 플랫폼이므로 Windows 에서도 그대로 동작한다.
func (r *Reloader) startSighupTrigger() {
	r.logger.Warn().Msg("sighup trigger unavailable on windows; ignored")
}
