//go:build js && wasm

// Package main is the GoWave WASM client entry point.
//
// Intentionally imports only syscall/js and the ui package.
// No encoding/json, no fmt, no net/http — TinyGo's stdlib shims
// for those packages pull in net/http which is broken in TinyGo 0.42
// for the js/wasm target.
package main

import (
	"syscall/js"

	"github.com/lolbaiteed/gowave/pkg/ui"
)

// Runtime owns the live component tree and tracks the previous render.
type Runtime struct {
	root     ui.Page
	prevHTML string
	appRoot  js.Value
}

var rt *Runtime

func main() {
	rt = &Runtime{}

	doc := js.Global().Get("document")
	rt.appRoot = doc.Call("getElementById", "app-root")
	if rt.appRoot.IsNull() || rt.appRoot.IsUndefined() {
		consoleWarn("[gowave] #app-root not found")
		select {}
	}

	// __gowave_dispatch(event, handlerID, value)
	// Called by the JS bridge on every DOM event.
	js.Global().Set("__gowave_dispatch", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 3 {
			return nil
		}
		ui.Dispatch(args[0].String(), args[1].String(), args[2].String())
		if rt.root != nil {
			rt.render()
		}
		return nil
	}))

	// __gowave_mount()
	// Called once by the JS bridge after WASM loads, to register handlers
	// against the SSR HTML already in the DOM.
	js.Global().Set("__gowave_mount", js.FuncOf(func(_ js.Value, _ []js.Value) any {
		if rt.root != nil {
			ui.ClearHandlers()
			_ = rt.root.Render()
		}
		return nil
	}))

	consoleLog("[gowave] WASM runtime ready")
	select {}
}

// Mount sets the active page and triggers the first client render.
// Called by generated page bootstrap code.
func Mount(page ui.Page) {
	if rt == nil {
		consoleWarn("[gowave] Mount called before runtime init")
		return
	}
	rt.root = page
	rt.render()
}

// render re-renders the component tree and applies the result to the DOM.
func (r *Runtime) render() {
	if r.root == nil {
		return
	}

	ui.ClearHandlers()
	newHTML := ui.RenderHTML(r.root.Render())

	if newHTML == r.prevHTML {
		return
	}

	if r.prevHTML == "" {
		// First client render — replace SSR placeholder directly.
		r.appRoot.Set("innerHTML", newHTML)
	} else {
		// Subsequent renders — send a full-replace patch to the JS bridge.
		// A tree-level differ will replace this in a future milestone.
		r.patch(newHTML)
	}

	r.prevHTML = newHTML
}

// patch sends a full-replace patch op to __gowave_patch in the JS bridge.
// Hand-encoded JSON to avoid importing encoding/json.
func (r *Runtime) patch(html string) {
	patchFn := js.Global().Get("__gowave_patch")
	if patchFn.IsUndefined() || patchFn.IsNull() {
		r.appRoot.Set("innerHTML", html)
		return
	}
	patchFn.Invoke(`[{"type":"full_render","html":` + jsonString(html) + `}]`)
}

// ── Minimal helpers ───────────────────────────────────────────────────────────

// jsonString encodes s as a JSON string literal without importing encoding/json.
// Escapes \  "  \n  \r  \t  <  >  &  to keep the output safe inside HTML.
func jsonString(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		case '<':
			out = append(out, '\\', 'u', '0', '0', '3', 'c')
		case '>':
			out = append(out, '\\', 'u', '0', '0', '3', 'e')
		case '&':
			out = append(out, '\\', 'u', '0', '0', '2', '6')
		default:
			out = append(out, c)
		}
	}
	out = append(out, '"')
	return string(out)
}

func consoleLog(msg string)  { js.Global().Get("console").Call("log", msg) }
func consoleWarn(msg string) { js.Global().Get("console").Call("warn", msg) }
