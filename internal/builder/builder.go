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
}

// Run executes a full production build:
//  1. Parse pages/ to discover routes
//  2. Generate and compile WASM entry point (standard Go)
//  3. Run SSR pre-render and write HTML shells
//  4. Copy public/ assets
func Run(cfg Config) error {
	start := time.Now()

	fmt.Printf("\n  gowave build\n")
	fmt.Printf("  output: %s/\n\n", cfg.OutDir)

	type step struct {
		name     string
		fn       func() error
		softFail bool
	}

	steps := []step{
		{"discover routes", func() error { return discoverRoutes(cfg) }, false},
		{"compile WASM", func() error { return compileWASM(cfg) }, true},
		{"SSR pre-render", func() error { return preRender(cfg) }, false},
		{"copy assets", func() error { return copyAssets(cfg) }, false},
	}

	for _, s := range steps {
		fmt.Printf("  %-20s", s.name+"...")
		if err := s.fn(); err != nil {
			if s.softFail {
				fmt.Printf("⚠ (skipped: %v)\n", err)
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
	m.Print()
	return parser.WriteManifest(m, filepath.Join(cfg.RootDir, cfg.OutDir))
}

// compileWASM generates a self-contained temp build directory, compiles it
// with standard Go (GOOS=js GOARCH=wasm), then removes the directory.
//
// The entry point imports pkg/ui from the gowave framework module.
// We locate the framework by reading the replace directive in the
// user project's go.mod.
func compileWASM(cfg Config) error {
	outDir := filepath.Join(cfg.RootDir, cfg.OutDir)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	outFile := filepath.Join(outDir, "main.wasm")

	// Find the gowave framework: module name + local path from replace directive.
	gw, err := readGowaveModule(cfg.RootDir)
	if err != nil {
		return fmt.Errorf("finding gowave module: %w", err)
	}

	// Build in a temp subdirectory so it doesn't conflict with the
	// project's own main.go (the HTTP server entry point).
	wasmDir := filepath.Join(cfg.RootDir, ".gowave-wasm")
	if err := os.MkdirAll(wasmDir, 0755); err != nil {
		return err
	}
	defer os.RemoveAll(wasmDir)

	// Standalone go.mod that replaces the gowave framework with local source.
	gomod := "module gowave_wasm_build\n\ngo 1.22\n\nrequire " + gw.moduleName +
		" v0.0.0-00010101000000-000000000000\n\nreplace " + gw.moduleName +
		" => " + gw.localPath + "\n"

	if err := os.WriteFile(filepath.Join(wasmDir, "go.mod"), []byte(gomod), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(wasmDir, "go.sum"), nil, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(wasmDir, "main.go"), []byte(wasmEntry(gw.moduleName)), 0644); err != nil {
		return err
	}

	// Tidy the generated module before building.
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = wasmDir
	tidy.Env = os.Environ()
	if out, err := tidy.CombinedOutput(); err != nil {
		return fmt.Errorf("go mod tidy: %s", strings.TrimSpace(string(out)))
	}

	var stderr strings.Builder
	cmd := exec.Command("go", "build", "-o", outFile, ".")
	cmd.Dir = wasmDir
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

// gowaveRef holds the resolved gowave framework module name and local path.
type gowaveRef struct {
	moduleName string // e.g. github.com/lolbaiteed/gowave
	localPath  string // absolute path to the framework source
}

// readGowaveModule finds the gowave framework by reading the replace directive
// from the user project's go.mod:
//
//	require github.com/lolbaiteed/gowave v0.0.0-...
//	replace github.com/lolbaiteed/gowave => /home/misato/personal/gowave
func readGowaveModule(projectDir string) (*gowaveRef, error) {
	data, err := os.ReadFile(filepath.Join(projectDir, "go.mod"))
	if err != nil {
		return nil, err
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "replace ") {
			continue
		}
		// "replace <module> => <path>"
		parts := strings.Fields(line)
		if len(parts) < 4 || parts[2] != "=>" {
			continue
		}
		moduleName := parts[1]
		localPath := parts[3]

		if !filepath.IsAbs(localPath) {
			localPath = filepath.Join(projectDir, localPath)
		}

		// Verify it looks like a gowave source tree (has pkg/ui).
		if _, err := os.Stat(filepath.Join(localPath, "pkg", "ui")); err != nil {
			continue
		}

		return &gowaveRef{moduleName: moduleName, localPath: localPath}, nil
	}

	return nil, fmt.Errorf(
		"no replace directive pointing to gowave source found in go.mod\n" +
			"add to your go.mod:\n" +
			"  require github.com/yourname/gowave v0.0.0-00010101000000-000000000000\n" +
			"  replace github.com/yourname/gowave => /path/to/gowave")
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

// wasmEntry returns the generated WASM entry point source.
// It imports pkg/ui from the gowave framework module.
func wasmEntry(gowaveModule string) string {
	return `//go:build js && wasm

package main

import (
	"syscall/js"
	"` + gowaveModule + `/pkg/ui"
)

type gwRuntime struct {
	root     ui.Page
	prevHTML string
	appRoot  js.Value
}

var rt *gwRuntime

func main() {
	rt = &gwRuntime{}
	doc := js.Global().Get("document")
	rt.appRoot = doc.Call("getElementById", "app-root")
	if rt.appRoot.IsNull() || rt.appRoot.IsUndefined() {
		js.Global().Get("console").Call("warn", "[gowave] #app-root not found")
		select {}
	}
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
	js.Global().Set("__gowave_mount", js.FuncOf(func(_ js.Value, _ []js.Value) any {
		if rt.root != nil {
			ui.ClearHandlers()
			_ = rt.root.Render()
		}
		return nil
	}))
	js.Global().Get("console").Call("log", "[gowave] WASM runtime ready")
	select {}
}

func (r *gwRuntime) render() {
	if r.root == nil {
		return
	}
	ui.ClearHandlers()
	newHTML := ui.RenderHTML(r.root.Render())
	if newHTML == r.prevHTML {
		return
	}
	if r.prevHTML == "" {
		r.appRoot.Set("innerHTML", newHTML)
	} else {
		patchFn := js.Global().Get("__gowave_patch")
		if patchFn.IsUndefined() || patchFn.IsNull() {
			r.appRoot.Set("innerHTML", newHTML)
		} else {
			patchFn.Invoke("[{\"type\":\"full_render\",\"html\":" + gwJSON(newHTML) + "}]")
		}
	}
	r.prevHTML = newHTML
}

func gwJSON(s string) string {
	b := make([]byte, 0, len(s)+2)
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			b = append(b, s[i])
		}
	}
	return string(append(b, '"'))
}
`
}

func preRender(cfg Config) error {
	outDir := filepath.Join(cfg.RootDir, cfg.OutDir, "_ssr")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	const html = "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n" +
		"  <meta charset=\"utf-8\">\n" +
		"  <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n" +
		"  <title>GoWave App</title>\n" +
		"  <script src=\"/wasm_exec.js\" defer></script>\n" +
		"  <script src=\"/gowave.js\" defer></script>\n" +
		"</head>\n<body>\n" +
		"  <div id=\"app-root\"></div>\n" +
		"</body>\n</html>\n"
	return os.WriteFile(filepath.Join(outDir, "index.html"), []byte(html), 0644)
}

func copyAssets(cfg Config) error {
	src := filepath.Join(cfg.RootDir, "public")
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil
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
