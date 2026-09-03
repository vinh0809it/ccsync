//go:build desktop

package gui

import (
	"net/http"
	"runtime"

	webview "github.com/webview/webview_go"
)

// DesktopSupported reports whether this binary was built with -tags desktop.
const DesktopSupported = true

// RunWindow serves the GUI and shows it in a native WebKit window instead
// of the browser. Blocks until the window is closed.
func RunWindow(port int) error {
	// The webview event loop must run on the process's main thread.
	runtime.LockOSThread()

	ln, s, url, err := Listen(port)
	if err != nil {
		return err
	}
	go http.Serve(ln, s.Handler())

	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle("ccsync — Claude Code session sync")
	w.SetSize(1120, 760, webview.HintNone)
	w.Navigate(url)
	w.Run()
	return nil
}
