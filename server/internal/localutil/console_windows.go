//go:build windows

package localutil

import "syscall"

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	user32            = syscall.NewLazyDLL("user32.dll")
	procAllocConsole  = kernel32.NewProc("AllocConsole")
	procGetConsoleWnd = kernel32.NewProc("GetConsoleWindow")
	procShowWindow    = user32.NewProc("ShowWindow")
)

func EnsureHiddenConsole() {
	if window, _, _ := procGetConsoleWnd.Call(); window != 0 {
		return
	}
	if allocated, _, _ := procAllocConsole.Call(); allocated == 0 {
		return
	}
	if window, _, _ := procGetConsoleWnd.Call(); window != 0 {
		procShowWindow.Call(window, 0)
	}
}
