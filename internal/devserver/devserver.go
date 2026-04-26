package devserver

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lolbaiteed/gowave/internal/builder"
	"github.com/lolbaiteed/gowave/internal/watcher"
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
}

func (s *devServer) start() error {
	// Validate we're in a gowave project
	if _, err := os.Stat(filepath.Join(s.cfg.RootDir, "gowave.toml")); os.IsNotExist(err) {
		return fmt.Errorf("no gowave.toml found — are you in a GoWave project directory?")
	}

	// Initial build
	fmt.Printf("\n  gowave dev → http://localhost:%s\n\n", s.cfg.Port)
	s.rebuild()

	// Start file watcher
	w := watcher.New(s.cfg.RootDir, 400*time.Millisecond, func(ev watcher.Event) {
		ext := filepath.Ext(ev.Path)
		if ext == ".go" || ext == ".toml" {
			rel, _ := filepath.Rel(s.cfg.RootDir, ev.Path)
			fmt.Printf("  changed: %s — rebuilding...\n", rel)
			s.rebuild()
			s.broadcast("reload")
		}
	})
	w.Start()
	defer w.Stop()

	mux := http.NewServeMux()

	// SSE hot-reload endpoint
	mux.HandleFunc("/_gowave/reload", s.handleSSE)

	// WASM bundle
	mux.HandleFunc("/_wasm/", s.handleWASM)

	// TinyGo's wasm_exec.js (bundled with gowave)
	mux.HandleFunc("/wasm_exec.js", s.handleWasmExec)

	// Public assets
	mux.Handle("/public/", http.StripPrefix("/public/",
		http.FileServer(http.Dir(filepath.Join(s.cfg.RootDir, "public"))),
	))

	// gowave.js bridge
	mux.HandleFunc("/gowave.js", func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(s.cfg.RootDir, "public", "gowave.js")
		if _, err := os.Stat(p); err == nil {
			http.ServeFile(w, r, p)
			return
		}
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, devBridgeScript)
	})

	// All other routes → SSR HTML shell with hot-reload injected
	mux.HandleFunc("/", s.handlePage)

	log.Printf("  watching for changes...\n\n")
	return http.ListenAndServe(":"+s.cfg.Port, mux)
}

func (s *devServer) rebuild() {
	cfg := builder.Config{
		RootDir: s.cfg.RootDir,
		OutDir:  ".gowave-cache",
		Target:  "tinygo",
	}
	start := time.Now()
	if err := builder.Run(cfg); err != nil {
		fmt.Printf("  build error: %v\n", err)
		return
	}
	fmt.Printf("  ready in %s\n", time.Since(start).Round(time.Millisecond))
}

func (s *devServer) handlePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, s.buildDevHTML(r.URL.Path))
}

func (s *devServer) buildDevHTML(route string) string {
	title := "GoWave dev"
	if route != "/" {
		title = strings.Trim(route, "/") + " — GoWave"
	}
	return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>` + title + `</title>
  <script src="/wasm_exec.js"></script>
</head>
<body>
  <div id="app-root">
    <!-- SSR placeholder: in production this is pre-rendered Go HTML -->
    <div style="font-family:monospace;padding:2rem;color:#888">
      booting gowave runtime…
    </div>
  </div>
  <script src="/gowave.js"></script>

  <!-- Hot reload via SSE -->
  <script>
    (function() {
      const es = new EventSource('/_gowave/reload');
      es.addEventListener('reload', () => {
        console.log('[gowave] reloading…');
        location.reload();
      });
      es.addEventListener('error', () => {
        console.log('[gowave] dev server disconnected');
      });
    })();
  </script>
</body>
</html>`
}

func (s *devServer) handleWASM(w http.ResponseWriter, r *http.Request) {
	wasmPath := filepath.Join(s.cfg.RootDir, ".gowave-cache", "main.wasm")
	if _, err := os.Stat(wasmPath); os.IsNotExist(err) {
		http.Error(w, "WASM not built yet", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/wasm")
	http.ServeFile(w, r, wasmPath)
}

func (s *devServer) handleWasmExec(w http.ResponseWriter, r *http.Request) {
	// In production this would be the embedded TinyGo wasm_exec.js.
	// For now, serve a stub that tells the user to install TinyGo.
	w.Header().Set("Content-Type", "application/javascript")
	fmt.Fprint(w, wasmExecStub)
}

// handleSSE is the Server-Sent Events endpoint for hot reload.
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

	// Send initial ping to confirm connection
	fmt.Fprintf(w, "event: ping\ndata: connected\n\n")
	flusher.Flush()

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

// devBridgeScript is the inline dev-mode WASM bridge.
// It mirrors what public/gowave.js does when present.
const devBridgeScript = `
/* gowave.js — dev mode bridge */
(async () => {
  const go = new globalThis.Go?.() ?? null;
  if (!go) {
    console.warn('[gowave] wasm_exec.js not loaded — TinyGo runtime unavailable');
    document.getElementById('app-root').innerHTML =
      '<div style="font-family:monospace;padding:2rem">' +
      '<b>GoWave dev server running.</b><br><br>' +
      'Install <a href="https://tinygo.org/getting-started/">TinyGo</a> to compile your components to WASM.<br>' +
      'Until then, SSR HTML is served directly.' +
      '</div>';
    return;
  }
  try {
    const result = await WebAssembly.instantiateStreaming(
      fetch('/_wasm/main.wasm'), go.importObject
    );
    go.run(result.instance);
  } catch(e) {
    console.error('[gowave] WASM load failed:', e);
  }

  globalThis.__gowave_patch = (patchJSON) => {
    const patches = JSON.parse(patchJSON);
    for (const op of patches) {
      const el = document.querySelector('[data-gw-id="' + op.id + '"]');
      if (!el) continue;
      if (op.type === 'set_text') el.textContent = op.value;
      if (op.type === 'set_attr') el.setAttribute(op.key, op.value);
      if (op.type === 'replace')  el.outerHTML = op.html;
    }
  };

  document.addEventListener('click', e => {
    const id = e.target.dataset?.gwClick;
    if (id && globalThis.__gowave_dispatch) globalThis.__gowave_dispatch('click', id, '');
  });
  document.addEventListener('input', e => {
    const id = e.target.dataset?.gwInput;
    if (id && globalThis.__gowave_dispatch) globalThis.__gowave_dispatch('input', id, e.target.value);
  });
})();
`

// wasmExecStub — real wasm_exec.js is installed alongside TinyGo.
// This stub avoids a 404 and shows a useful error.
const wasmExecStub = `
/* wasm_exec.js stub — real file ships with TinyGo */
if (!globalThis.Go) {
  globalThis.Go = class {
    constructor() { this.importObject = { env: {}, wasi_snapshot_preview1: {} }; }
    run() {}
  };
  console.warn('[gowave] TinyGo not installed — WASM compilation unavailable.');
  console.warn('[gowave] Install TinyGo: https://tinygo.org/getting-started/');
}
`
