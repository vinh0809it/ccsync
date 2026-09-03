//go:build !desktop

package gui

import "fmt"

// DesktopSupported reports whether this binary was built with -tags desktop.
const DesktopSupported = false

// RunWindow is unavailable in this build; callers should fall back to Run.
func RunWindow(port int) error {
	return fmt.Errorf("this build has no desktop window support (rebuild with -tags desktop)")
}
