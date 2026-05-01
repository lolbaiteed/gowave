//go:build js && wasm

// Package main is the GoWave WASM client entry point.
//
// This file only compiles when targeting js/wasm (via TinyGo or go build
// with GOOS=js GOARCH=wasm). It:
//
//  1. Exposes __gowave_dispatch to JavaScript so the bridge can route events
//  2. Mounts the root component into #app-root
//  3. Runs a render loop: on every state mutation, re-renders and patches the DOM
//
// The separation is clean: the Go component structs know nothing about WASM.
// Only this file touches syscall/js.
package main

import (
	"encoding/json"
	"fmt"
	"syscall/js"

	"github.com/gowave/gowave/pkg/ui"
)

// Runtime is the WASM client runtime.
// It owns the live component tree and the current rendered DOM state.
type Runtime struct {
	root     ui.Page         // the current mounted page component
	prevHTML string          // last rendered HTML (for diffing)
	appRoot  js.Value        // #app-root DOM element
}

var rt *Runtime

func main() {
	rt = &Runtime{}

	// Get the #app-root mount point
	doc := js.Global().Get("document")
	rt.appRoot = doc.Call("getElementById", "app-root")
	if rt.appRoot.IsNull() || rt.appRoot.IsUndefined() {
		fmt.Println("[gowave] #app-root not found — is the HTML shell loaded?")
		// Block forever so the WASM binary stays alive
		select {}
	}

	// Expose __gowave_dispatch(event, handlerID, value) for the JS bridge
	js.Global().Set("__gowave_dispatch", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) < 3 {
			return nil
		}
		event := args[0].String()
		id := args[1].String()
		value := args[2].String()

		// Dispatch the event to the Go handler registry
		ui.Dispatch(event, id, value)

		// Re-render after any state mutation
		if rt.root != nil {
			rt.render()
		}
		return nil
	}))

	// Expose __gowave_mount(componentJSON) — called by the JS bridge
	// once the WASM is ready, to hydrate the server-rendered HTML.
	js.Global().Set("__gowave_mount", js.FuncOf(func(this js.Value, args []js.Value) any {
		// Initial hydration: the SSR HTML is already in the DOM.
		// We just need to register the current component so event
		// dispatch knows what to re-render.
		if rt.root != nil {
			// Re-render to register all handlers for the current state.
			ui.ClearHandlers()
			_ = rt.root.Render()
		}
		return nil
	}))

	fmt.Println("[gowave] WASM runtime ready")

	// Block forever — Go callbacks keep the runtime alive.
	select {}
}

// Mount sets the active page component and renders it.
// Called by the generated page bootstrap code.
func Mount(page ui.Page) {
	if rt == nil {
		fmt.Println("[gowave] Mount called before runtime init")
		return
	}
	rt.root = page
	rt.render()
}

// render re-renders the current component and patches the DOM.
func (r *Runtime) render() {
	if r.root == nil {
		return
	}

	ui.ClearHandlers()
	node := r.root.Render()
	newHTML := ui.RenderHTML(node)

	if newHTML == r.prevHTML {
		return // nothing changed
	}

	patches := diff(r.prevHTML, newHTML)

	if len(patches) == 0 || r.prevHTML == "" {
		// First render or full replace — set innerHTML directly
		r.appRoot.Set("innerHTML", newHTML)
	} else {
		// Apply patches via the JS bridge
		r.applyPatches(patches)
	}

	r.prevHTML = newHTML
}

// ── Patch protocol ────────────────────────────────────────────────────────────

// PatchOp is a single DOM mutation instruction.
type PatchOp struct {
	Type  string `json:"type"`
	ID    string `json:"id,omitempty"`
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
	HTML  string `json:"html,omitempty"`
}

// diff computes the minimal set of patch ops between two HTML strings.
// For the initial milestone this is a simple full-replace strategy.
// A real differ (tree-level) is the next iteration.
func diff(prev, next string) []PatchOp {
	if prev == "" {
		return nil // first render — handled by innerHTML assignment above
	}
	if prev == next {
		return nil
	}
	// Full replace for now. The VDOM differ replaces this in the next milestone.
	return []PatchOp{
		{Type: "full_render", HTML: next},
	}
}

// applyPatches sends patch ops to the JS bridge for DOM application.
func (r *Runtime) applyPatches(patches []PatchOp) {
	data, err := json.Marshal(patches)
	if err != nil {
		fmt.Printf("[gowave] patch marshal error: %v\n", err)
		return
	}

	patchFn := js.Global().Get("__gowave_patch")
	if patchFn.IsUndefined() || patchFn.IsNull() {
		// Fallback: just set innerHTML
		r.appRoot.Set("innerHTML", patches[len(patches)-1].HTML)
		return
	}
	patchFn.Invoke(string(data))
}
