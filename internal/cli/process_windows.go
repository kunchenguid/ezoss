//go:build windows

package cli

import (
	"errors"

	"golang.org/x/sys/windows"
)

func processExists(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err == nil {
		_ = windows.CloseHandle(handle)
		return true
	}
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return true
	}
	return false
}
