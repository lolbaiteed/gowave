// Package devserver runs the GoWave development server.
//
// It:
//   - Builds the route manifest on startup via the parser
//   - Serves SSR-rendered HTML via the ssr.Router for each route
//   - Serves the compiled WASM bundle at /_wasm/main.wasm
//   - Injects hot-reload SSE into every page
//   - Watches .go files and rebuilds + broadcasts reload on change
package devserver

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lolbaiteed/gowave/internal/builder"
	"github.com/lolbaiteed/gowave/internal/parser"
	"github.com/lolbaiteed/gowave/internal/ssr"
	"github.com/lolbaiteed/gowave/internal/watcher"
	"github.com/lolbaiteed/gowave/pkg/ui"
)

// Config controls the dev server.
type Config struct {
	Port    string
	RootDir string
}

// Run starts the dev server and blocks until it exits.
func Run(cfg Config) error {
	srv := &devServer{
		cfg:     cfg,
		clients: make(map[chan string]struct{}),
	}
	return srv.start()
}

type devServer struct {
	cfg     Config
	mu      sync.Mutex
	clients map[chan string]struct{}

	routerMu sync.RWMutex
	router   *ssr.Router
}

func (s *devServer) start() error {
	if _, err := os.Stat(filepath.Join(s.cfg.RootDir, "gowave.toml")); os.IsNotExist(err) {
		return fmt.Errorf("no gowave.toml found — are you in a GoWave project?")
	}

	fmt.Printf("\n  gowave dev → http://localhost:%s\n\n", s.cfg.Port)

	if err := s.rebuild(); err != nil {
		fmt.Printf("  warn: initial build failed: %v\n", err)
	}

	w := watcher.New(s.cfg.RootDir, 400*time.Millisecond, func(ev watcher.Event) {
		ext := filepath.Ext(ev.Path)
		if (ext == ".go" || ext == ".toml") && !strings.HasPrefix(filepath.Base(ev.Path), "_gowave_") {
			rel, _ := filepath.Rel(s.cfg.RootDir, ev.Path)
			fmt.Printf("  changed: %s — rebuilding...\n", rel)
			if err := s.rebuild(); err != nil {
				fmt.Printf("  build error: %v\n", err)
			} else {
				s.broadcast("reload")
			}
		}
	})
	w.Start()
	defer w.Stop()

	mux := http.NewServeMux()
	mux.HandleFunc("/_gowave/reload", s.handleSSE)
	mux.HandleFunc("/_wasm/", s.handleWASM)
	mux.HandleFunc("/wasm_exec.js", s.handleWasmExec)
	mux.HandleFunc("/gowave.js", s.handleGowaveJS)
	mux.Handle("/public/", http.StripPrefix("/public/",
		http.FileServer(http.Dir(filepath.Join(s.cfg.RootDir, "public"))),
	))
	mux.HandleFunc("/", s.handlePage)

	log.Printf("  watching for changes...\n\n")
	return http.ListenAndServe(":"+s.cfg.Port, mux)
}

func (s *devServer) rebuild() error {
	start := time.Now()

	res, err := parser.ParsePages(s.cfg.RootDir)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	m := &parser.Manifest{Routes: res.Routes, Warnings: res.Warnings}
	cacheDir := filepath.Join(s.cfg.RootDir, ".gowave-cache")
	if err := parser.WriteManifest(m, cacheDir); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}

	for _, w := range res.Warnings {
		fmt.Printf("  warn: %s\n", w)
	}

	router := ssr.NewRouter(nil)
	for _, route := range res.Routes {
		router.Register(route.HTTPPath, makeDevPageFactory(route.HTTPPath, route.StructName))
	}

	s.routerMu.Lock()
	s.router = router
	s.routerMu.Unlock()

	go func() {
		bCfg := builder.Config{
			RootDir: s.cfg.RootDir,
			OutDir:  ".gowave-cache",
		}
		_ = builder.Run(bCfg)
	}()

	fmt.Printf("  routes registered in %s\n", time.Since(start).Round(time.Millisecond))
	m.Print()
	fmt.Println()
	return nil
}

func makeDevPageFactory(httpPath, structName string) func() ssr.Page {
	return func() ssr.Page {
		return &devPreviewPage{HTTPPath: httpPath, StructName: structName}
	}
}

type devPreviewPage struct {
	HTTPPath   string
	StructName string
}

func (p *devPreviewPage) Render() ui.Node {
	return ui.Div(ui.Class("container"),
		ui.Child(rawBadge("gowave dev")),
		ui.Child(ui.H1(ui.Text(p.StructName))),
		ui.Child(ui.P(ui.Textf("Route: %s", p.HTTPPath))),
		ui.Child(ui.P(ui.Text(
			"This page is live. Add a Render() method to your page struct and it will appear here once GoWave supports live compilation.",
		))),
		ui.Child(ui.Pre(ui.Child(ui.Code(ui.Textf(
			"// +gowave:page route=%q\ntype %s struct{}\n\nfunc (p *%s) Render() ui.Node {\n    return ui.Div(ui.Text(\"Hello!\"))\n}",
			p.HTTPPath, p.StructName, p.StructName,
		))))),
	)
}

func rawBadge(text string) ui.Node {
	return ui.Span(
		ui.Attr2("style", "font-family:monospace;font-size:11px;background:#e1f5ee;color:#085041;padding:3px 8px;border-radius:4px;"),
		ui.Text(text),
	)
}

func (s *devServer) handlePage(w http.ResponseWriter, r *http.Request) {
	s.routerMu.RLock()
	router := s.router
	s.routerMu.RUnlock()

	if router == nil {
		http.Error(w, "dev server starting — try again in a moment", http.StatusServiceUnavailable)
		return
	}
	router.ServeHTTP(w, r)
}

func (s *devServer) handleWASM(w http.ResponseWriter, r *http.Request) {
	wasmPath := filepath.Join(s.cfg.RootDir, ".gowave-cache", "main.wasm")
	if _, err := os.Stat(wasmPath); os.IsNotExist(err) {
		http.Error(w, "WASM not built yet", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/wasm")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, wasmPath)
}

func (s *devServer) handleWasmExec(w http.ResponseWriter, r *http.Request) {
	// Standard Go ships wasm_exec.js alongside the toolchain.
	// Find it via GOROOT which is always set when Go is installed.
	p := stdGoWasmExec()
	if p != "" {
		w.Header().Set("Content-Type", "application/javascript")
		http.ServeFile(w, r, p)
		return
	}
	w.Header().Set("Content-Type", "application/javascript")
	fmt.Fprint(w, wasmExecStub)
}

// stdGoWasmExec returns the path to wasm_exec.js bundled with the Go toolchain.
// It lives at $(go env GOROOT)/misc/wasm/wasm_exec.js.
func stdGoWasmExec() string {
	// Try go env GOROOT first — always correct regardless of install location.
	if out, err := exec.Command("go", "env", "GOROOT").Output(); err == nil {
		p := filepath.Join(strings.TrimSpace(string(out)), "misc", "wasm", "wasm_exec.js")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (s *devServer) handleGowaveJS(w http.ResponseWriter, r *http.Request) {
	p := filepath.Join(s.cfg.RootDir, "public", "gowave.js")
	if _, err := os.Stat(p); err == nil {
		http.ServeFile(w, r, p)
		return
	}
	w.Header().Set("Content-Type", "application/javascript")
	fmt.Fprint(w, devBridgeScript)
}

func (s *devServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan string, 4)
	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
	}()

	fmt.Fprintf(w, "event: ping\ndata: connected\n\n")
	flusher.(http.Flusher).Flush()

	for {
		select {
		case msg := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg, msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *devServer) broadcast(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- event:
		default:
		}
	}
}

const devBridgeScript = `
/* gowave.js — dev mode bridge */
// Wrap in DOMContentLoaded so this runs after all defer'd scripts
// (including wasm_exec.js) have executed, regardless of where in the
// document these script tags appear.
document.addEventListener('DOMContentLoaded', async () => {
  const GoConstructor = globalThis.Go;
  if (typeof GoConstructor !== 'function') {
    console.warn('[gowave] wasm_exec.js did not define globalThis.Go.');
    console.warn('[gowave] SSR is working. WASM interactivity needs Go toolchain in PATH.');
    return;
  }
  const go = new GoConstructor();
  try {
    const result = await WebAssembly.instantiateStreaming(fetch('/_wasm/main.wasm'), go.importObject);
    go.run(result.instance);
  } catch(e) {
    console.warn('[gowave] WASM load skipped:', e.message);
  }
  globalThis.__gowave_patch = (patchJSON) => {
    const patches = JSON.parse(patchJSON);
    for (const op of patches) {
      if (op.type === 'full_render') { document.getElementById('app-root').innerHTML = op.html; return; }
      const el = document.querySelector('[data-gw-id="' + op.id + '"]');
      if (!el) continue;
      if (op.type === 'set_text') el.textContent = op.value;
      if (op.type === 'set_attr')  el.setAttribute(op.key, op.value);
      if (op.type === 'replace')   el.outerHTML = op.html;
    }
  };
  document.addEventListener('click', e => {
    const id = e.target.dataset?.gwClick;
    if (id && globalThis.__gowave_dispatch) { e.preventDefault(); globalThis.__gowave_dispatch('click', id, ''); }
  });
  document.addEventListener('input', e => {
    const id = e.target.dataset?.gwInput;
    if (id && globalThis.__gowave_dispatch) globalThis.__gowave_dispatch('input', id, e.target.value);
  });
  document.addEventListener('keydown', e => {
    if (e.key !== 'Enter') return;
    const id = e.target.dataset?.gwEnter;
    if (id && globalThis.__gowave_dispatch) globalThis.__gowave_dispatch('enter', id, e.target.value);
  });
}); // end DOMContentLoaded
`

const wasmExecStub = `
/* wasm_exec.js stub — Go toolchain not found */
console.warn('[gowave] Could not find wasm_exec.js from Go toolchain. Ensure go is in PATH.');
`
