package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// Options controls what gowave new creates.
type Options struct {
	Name   string // project name, used in titles
	Dir    string // output directory
	Module string // Go module path
}

// Run scaffolds a full GoWave project into opts.Dir.
func Run(opts Options) error {
	if _, err := os.Stat(opts.Dir); err == nil {
		return fmt.Errorf("directory %q already exists", opts.Dir)
	}

	fmt.Printf("  creating %s/\n", opts.Dir)

	files := buildFileMap(opts)
	for path, content := range files {
		full := filepath.Join(opts.Dir, path)
		if err := writeFile(full, content, opts); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
		fmt.Printf("  + %s\n", path)
	}

	fmt.Printf("\nDone! Next steps:\n")
	fmt.Printf("  cd %s\n", opts.Dir)
	fmt.Printf("  gowave dev\n\n")
	return nil
}

func buildFileMap(opts Options) map[string]string {
	return map[string]string{
		"gowave.toml":        tmplGowaveToml,
		"go.mod":             tmplGoMod,
		"go.sum":             "",
		"main.go":            tmplMainGo,
		"layout.go":          tmplLayoutGo,
		"pages/index.go":     tmplIndexPage,
		"components/.keep":   "",
		"store/.keep":        "",
		"db/.keep":           "",
		"middleware/.keep":   "",
		"public/favicon.ico": "",
		"public/gowave.js":   tmplGowaveJS,
		".gitignore":         tmplGitignore,
		"README.md":          tmplReadme,
	}
}

func writeFile(path, content string, opts Options) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	rendered, err := renderTemplate(content, opts)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(rendered), 0644)
}

func renderTemplate(tmpl string, opts Options) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if err := t.Execute(&sb, opts); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// ── Templates ────────────────────────────────────────────────────────────────

const tmplGowaveToml = `# GoWave project configuration
module = "{{.Module}}"
name   = "{{.Name}}"
port   = "3000"
target = "tinygo"   # WASM compilation target
out    = "dist/"

[build]
  split_routes = true   # per-route WASM chunks
  minify       = false
`

const tmplGoMod = `module {{.Module}}

go 1.22

require (
	github.com/lolbaiteed/gowave v0.1.0
)
`

const tmplMainGo = `package main

import (
	"log"

	"github.com/lolbaiteed/gowave/pkg/runtime"
)

func main() {
	srv := runtime.NewServer(runtime.Config{
		Port:    "3000",
		RootDir: ".",
	})

	log.Printf("gowave dev → http://localhost:3000\n")

	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
`

const tmplLayoutGo = `package main

import "github.com/lolbaiteed/gowave/pkg/ui"

// RootLayout wraps every page. The slot argument is the rendered page content.
type RootLayout struct{}

func (l *RootLayout) Render(slot ui.Node) ui.Node {
	return ui.Html(
		ui.Head(
			ui.Meta("charset", "utf-8"),
			ui.Meta("viewport", "width=device-width, initial-scale=1"),
			ui.Title("{{.Name}}"),
			ui.Script("/gowave.js"),
		),
		ui.Body(
			ui.Div(ui.Class("app"), slot),
		),
	)
}
`

const tmplIndexPage = `package pages

import "github.com/lolbaiteed/gowave/pkg/ui"

// +gowave:page route="/"
type IndexPage struct {
	Count int
}

func (p *IndexPage) Render() ui.Node {
	return ui.Div(ui.Class("container"),
		ui.H1(ui.Text("Hello from GoWave")),
		ui.P(ui.Text("Go + WASM. No JavaScript written.")),
		ui.Button(
			ui.OnClick(p.Increment),
			ui.Textf("clicked %d times", p.Count),
		),
	)
}

func (p *IndexPage) Increment() {
	p.Count++
}
`

const tmplGowaveJS = `/**
 * gowave.js — WASM bootstrap bridge (~40kb in production)
 * This is the dev stub. The real file is emitted by 'gowave build'.
 *
 * Responsibilities:
 *   1. Fetch and instantiate the compiled .wasm bundle
 *   2. Set up the Go ↔ JS message channel
 *   3. Apply VDOM patches from Go to the live DOM
 *   4. Forward DOM events (clicks, input, etc.) back to Go
 */

(async () => {
  // In dev mode, the dev server serves the WASM bundle at /_wasm/main.wasm
  const wasmURL = '/_wasm/main.wasm';

  // Polyfill for environments without instantiateStreaming
  const instantiate = async (url, imports) => {
    if (WebAssembly.instantiateStreaming) {
      return WebAssembly.instantiateStreaming(fetch(url), imports);
    }
    const bytes = await fetch(url).then(r => r.arrayBuffer());
    return WebAssembly.instantiate(bytes, imports);
  };

  // The Go runtime exposes these via syscall/js
  const go = new globalThis.Go(); // injected by TinyGo's wasm_exec.js

  try {
    const { instance } = await instantiate(wasmURL, go.importObject);
    go.run(instance);
  } catch (e) {
    console.error('[gowave] failed to load WASM:', e);
  }

  // Patch channel: Go writes JSON patch ops, JS applies them
  globalThis.__gowave_patch = (patchJSON) => {
    const patches = JSON.parse(patchJSON);
    for (const op of patches) {
      applyPatch(op);
    }
  };

  // Event forwarding: JS captures events, calls Go handler by ID
  document.addEventListener('click', (e) => {
    const handlerId = e.target.dataset?.gwClick;
    if (handlerId && globalThis.__gowave_dispatch) {
      globalThis.__gowave_dispatch('click', handlerId, '');
    }
  });

  document.addEventListener('input', (e) => {
    const handlerId = e.target.dataset?.gwInput;
    if (handlerId && globalThis.__gowave_dispatch) {
      globalThis.__gowave_dispatch('input', handlerId, e.target.value);
    }
  });

  function applyPatch(op) {
    switch (op.type) {
      case 'set_text': {
        const el = document.querySelector('[data-gw-id="' + op.id + '"]');
        if (el) el.textContent = op.value;
        break;
      }
      case 'set_attr': {
        const el = document.querySelector('[data-gw-id="' + op.id + '"]');
        if (el) el.setAttribute(op.key, op.value);
        break;
      }
      case 'replace': {
        const el = document.querySelector('[data-gw-id="' + op.id + '"]');
        if (el) el.outerHTML = op.html;
        break;
      }
      case 'full_render': {
        document.getElementById('app-root').innerHTML = op.html;
        break;
      }
    }
  }
})();
`

const tmplGitignore = `dist/
*.wasm
*.test
vendor/
.DS_Store
`

const tmplReadme = `# {{.Name}}

Built with [GoWave](https://github.com/lolbaiteed/gowave) — Go + WebAssembly, no JS tax.

## Commands

` + "```" + `bash
gowave dev        # start dev server with hot reload → http://localhost:3000
gowave build      # compile to WASM + SSR bundle → dist/
` + "```" + `

## Structure

` + "```" + `
{{.Name}}/
  pages/          # file-based routes  (pages/index.go → /)
  components/     # shared UI components
  store/          # global shared state
  db/             # database layer (server-only)
  middleware/     # HTTP middleware
  public/         # static assets
  layout.go       # root HTML shell
  main.go         # server entrypoint
  gowave.toml     # project config
` + "```"
