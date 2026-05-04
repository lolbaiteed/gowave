// Package builder compiles a GoWave project to WASM + SSR bundle.
package builder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lolbaiteed/gowave/internal/parser"
)

// Config controls the build.
type Config struct {
	RootDir string
	OutDir  string
	Target  string // "tinygo" | "go"
	Minify  bool
}

// Run executes a full production build:
//  1. Parse pages/ to discover routes
//  2. Compile each route to WASM via TinyGo (or go tool compile)
//  3. Run SSR pre-render and write HTML shells
//  4. Copy public/ assets and emit gowave.js bridge
func Run(cfg Config) error {
	start := time.Now()

	fmt.Printf("\n  gowave build\n")
	fmt.Printf("  target: %s\n", cfg.Target)
	fmt.Printf("  output: %s/\n\n", cfg.OutDir)

	type step struct {
		name     string
		fn       func() error
		softFail bool // if true, log warning but continue on error
	}

	steps := []step{
		{"discover routes", func() error { return discoverRoutes(cfg) }, false},
		{"compile WASM", func() error { return compileWASM(cfg) }, true},
		{"SSR pre-render", func() error { return preRender(cfg) }, false},
		{"copy assets", func() error { return copyAssets(cfg) }, false},
		{"emit bridge", func() error { return emitBridge(cfg) }, false},
	}

	for _, s := range steps {
		fmt.Printf("  %-20s", s.name+"...")
		if err := s.fn(); err != nil {
			if s.softFail {
				fmt.Printf("⚠ (skipped)\n")
			} else {
				fmt.Printf("✗\n")
				return fmt.Errorf("step %q failed: %w", s.name, err)
			}
		} else {
			fmt.Printf("✓\n")
		}
	}

	fmt.Printf("\n  built in %s → %s/\n\n", time.Since(start).Round(time.Millisecond), cfg.OutDir)
	return nil
}

func discoverRoutes(cfg Config) error {
	res, err := parser.ParsePages(cfg.RootDir)
	if err != nil {
		return err
	}

	m := &parser.Manifest{Routes: res.Routes, Warnings: res.Warnings}

	// Print route table
	m.Print()

	// Write routes.json to cache dir for SSR renderer and dev server
	cacheDir := filepath.Join(cfg.RootDir, cfg.OutDir)
	if err := parser.WriteManifest(m, cacheDir); err != nil {
		return fmt.Errorf("writing route manifest: %w", err)
	}

	return nil
}

func compileWASM(cfg Config) error {
	outDir := filepath.Join(cfg.RootDir, cfg.OutDir)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	outFile := filepath.Join(outDir, "main.wasm")

	// Read the user project's module name from their go.mod
	moduleName, err := readModuleName(cfg.RootDir)
	if err != nil {
		return fmt.Errorf("reading go.mod: %w", err)
	}

	// Generate a temporary WASM entry point in the user's project.
	// This avoids hardcoding the gowave module path inside cmd/wasm.
	entryFile := filepath.Join(cfg.RootDir, "_gowave_wasm_main.go")
	if err := os.WriteFile(entryFile, []byte(wasmEntryTemplate(moduleName)), 0644); err != nil {
		return fmt.Errorf("writing wasm entry: %w", err)
	}
	defer os.Remove(entryFile) // always clean up

	switch cfg.Target {
	case "tinygo":
		return compileTinyGo(cfg.RootDir, outFile)
	default:
		return compileStdGoWASM(cfg.RootDir, outFile)
	}
}

// readModuleName reads the module declaration from go.mod in dir.
func readModuleName(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", fmt.Errorf("module declaration not found in go.mod")
}

// wasmEntryTemplate returns a generated WASM entry point that imports
// the user project's own module path for pkg/ui.
// This file is written temporarily during build and deleted after.
func wasmEntryTemplate(moduleName string) string {
	return `//go:build js && wasm

package main

import (
	"syscall/js"
	"` + moduleName + `/pkg/ui"
)

type gwRuntime struct {
	root     ui.Page
	prevHTML string
	appRoot  js.Value
}

var gwRT *gwRuntime

func main() {
	gwRT = &gwRuntime{}
	doc := js.Global().Get("document")
	gwRT.appRoot = doc.Call("getElementById", "app-root")
	if gwRT.appRoot.IsNull() || gwRT.appRoot.IsUndefined() {
		js.Global().Get("console").Call("warn", "[gowave] #app-root not found")
		select {}
	}
	js.Global().Set("__gowave_dispatch", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 3 { return nil }
		ui.Dispatch(args[0].String(), args[1].String(), args[2].String())
		if gwRT.root != nil { gwRT.gwRender() }
		return nil
	}))
	js.Global().Set("__gowave_mount", js.FuncOf(func(_ js.Value, _ []js.Value) any {
		if gwRT.root != nil { ui.ClearHandlers(); _ = gwRT.root.Render() }
		return nil
	}))
	js.Global().Get("console").Call("log", "[gowave] WASM runtime ready")
	select {}
}

func (r *gwRuntime) gwRender() {
	if r.root == nil { return }
	ui.ClearHandlers()
	newHTML := ui.RenderHTML(r.root.Render())
	if newHTML == r.prevHTML { return }
	if r.prevHTML == "" {
		r.appRoot.Set("innerHTML", newHTML)
	} else {
		patchFn := js.Global().Get("__gowave_patch")
		if patchFn.IsUndefined() || patchFn.IsNull() {
			r.appRoot.Set("innerHTML", newHTML)
		} else {
			patchFn.Invoke(` + "`" + `[{"type":"full_render","html":` + "`" + ` + gwJSONString(newHTML) + ` + "`" + `}]` + "`" + `)
		}
	}
	r.prevHTML = newHTML
}

func gwJSONString(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':  out = append(out, '\', '"')
		case '\': out = append(out, '\', '\')
		case '
': out = append(out, '\', 'n')
		case '
': out = append(out, '\', 'r')
		case '	': out = append(out, '\', 't')
		default:   out = append(out, c)
		}
	}
	return string(append(out, '"'))
}
`
}

// WASMResult describes the outcome of a WASM compilation attempt.
type WASMResult struct {
	OK      bool
	Skipped bool   // TinyGo not found
	Err     error  // compilation failed but non-fatal
	Hint    string // user-facing advice
}

func compileTinyGo(rootDir, outFile string) error {
	res := tryCompileTinyGo(rootDir, outFile)
	if res.Skipped {
		fmt.Printf("\n    ⚠  TinyGo not installed — SSR works, WASM interactivity disabled")
		fmt.Printf("\n       Install: https://tinygo.org/getting-started/\n    ")
		return nil // soft-fail: SSR still works without WASM
	}
	if !res.OK {
		fmt.Printf("\n    ⚠  TinyGo compile failed — SSR still works")
		if res.Hint != "" {
			fmt.Printf("\n       hint: %s", res.Hint)
		}
		fmt.Printf("\n       %v\n    ", res.Err)
		return nil // soft-fail: don't break gowave dev over WASM
	}
	return nil
}

func tryCompileTinyGo(rootDir, outFile string) WASMResult {
	if _, err := exec.LookPath("tinygo"); err != nil {
		return WASMResult{Skipped: true}
	}

	var stderr strings.Builder
	cmd := exec.Command("tinygo", "build",
		"-o", outFile,
		"-target", "wasm",
		"-no-debug",
		".",
	)
	cmd.Dir = rootDir
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := stderr.String()
		hint := diagnoseWASMError(stderrStr)

		// TinyGo 0.42 has a broken net/http shim for js/wasm.
		// Fall back to standard Go WASM automatically.
		if strings.Contains(stderrStr, "roundtrip_js.go") ||
			strings.Contains(stderrStr, "roundTrip undefined") {
			fmt.Printf("\n    TinyGo net/http bug detected — falling back to standard Go WASM\n    ")
			return tryCompileStdGo(rootDir, outFile)
		}

		return WASMResult{Err: err, Hint: hint}
	}
	return WASMResult{OK: true}
}

func tryCompileStdGo(rootDir, outFile string) WASMResult {
	var stderr strings.Builder
	cmd := exec.Command("go", "build", "-o", outFile, "./cmd/wasm")
	cmd.Dir = rootDir
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return WASMResult{Err: fmt.Errorf("std go wasm: %w", err), Hint: stderr.String()}
	}
	fmt.Printf("\n    note: using standard Go WASM (~2MB). TinyGo gives smaller bundles once fixed.\n    ")
	return WASMResult{OK: true}
}

// diagnoseWASMError inspects TinyGo stderr and returns a human-friendly hint.
func diagnoseWASMError(stderr string) string {
	switch {
	case strings.Contains(stderr, "no such file or directory") && strings.Contains(stderr, "compiler-rt"):
		return "TinyGo can't find compiler-rt. On Fedora: sudo dnf install clang compiler-rt"
	case strings.Contains(stderr, "clang") && strings.Contains(stderr, "not found"):
		return "TinyGo needs clang. On Fedora: sudo dnf install clang"
	case strings.Contains(stderr, "no such file or directory") && strings.Contains(stderr, "clang"):
		return "clang version mismatch. Run: tinygo env CLANG and ensure that binary exists"
	case strings.Contains(stderr, "syscall/js"):
		return "add //go:build js && wasm build tag to WASM-only files"
	case strings.Contains(stderr, "go.sum"):
		return "run go mod tidy in your project first"
	default:
		if len(stderr) > 200 {
			return stderr[:200] + "..."
		}
		return stderr
	}
}

func compileStdGoWASM(rootDir, outFile string) error {
	cmd := exec.Command("go", "build",
		"-o", outFile,
		".",
	)
	cmd.Dir = rootDir
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func preRender(cfg Config) error {
	outDir := filepath.Join(cfg.RootDir, cfg.OutDir, "_ssr")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	// Write the index HTML shell that the WASM runtime hydrates into
	html := buildHTMLShell("", "") // module path and app name resolved at runtime
	return os.WriteFile(filepath.Join(outDir, "index.html"), []byte(html), 0644)
}

func copyAssets(cfg Config) error {
	src := filepath.Join(cfg.RootDir, "public")
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil // no public/ dir is fine
	}
	dst := filepath.Join(cfg.RootDir, cfg.OutDir)
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
}

func emitBridge(cfg Config) error {
	// In a real build this would embed the production gowave.js
	// For now, copy from public/ if it exists, or write the dev stub
	dst := filepath.Join(cfg.RootDir, cfg.OutDir, "gowave.js")
	src := filepath.Join(cfg.RootDir, "public", "gowave.js")
	if data, err := os.ReadFile(src); err == nil {
		return os.WriteFile(dst, data, 0644)
	}
	return os.WriteFile(dst, []byte(productionBridgeStub), 0644)
}

func buildHTMLShell(modulePath, appName string) string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>GoWave App</title>
  <script src="/wasm_exec.js"></script>
  <script src="/gowave.js"></script>
</head>
<body>
  <div id="app-root"><!-- SSR content injected here --></div>
</body>
</html>
`
}

const productionBridgeStub = `/* gowave.js production bridge — emitted by 'gowave build' */`
