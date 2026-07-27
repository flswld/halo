//go:build darwin && !cgo
// +build darwin,!cgo

package logger

// getThreadId 在不启用 CGO 的 Darwin 平台返回不可用标记
func (l *Logger) getThreadId() (threadId string) {
	return "N/A"
}
