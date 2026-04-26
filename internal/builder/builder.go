package builder

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Config struct {
	RootDir string
	OutDir  string
	Target  string
	Minify  bool
}

func Run(cfg Config) error {
	start := time.Now()

	fmt.Printf("\n gowave build\n")
	fmt.Printf("  target: %s\n", cfg.Target)
	fmt.Printf("  output: %s/\n\n", cfg.OutDir)

	steps := []struct {
		name string
		fn   func() error
	}{
		{"discover routes", func() error { return discoverRoutes(cfg) }},
		{"compile WASM", func() error { return compileWASM(cfg) }},
		{"SSR pre-render", func() error { return preRender(cfg) }},
		{"Copy assets", func() error { return copyAssets(cfg) }},
		{"emit bridge", func() error { return emitBridge(cfg) }},
	}

	for _, step := range steps {
		fmt.Printf("  %-20s", step.name+"...")
		if err := step.fn(); err != nil {
			fmt.Printf("x\n")
			return fmt.Errorf("step %q failed: %w", step.name, err)
		}
		fmt.Printf("✓\n")
	}

	fmt.Printf("\n built in %s -> %s/\n\n", time.Since(start).Round(time.Millisecond), cfg.OutDir)
	return nil
}

func discoverRoutes(cfg Config) error {
	pagesDir := filepath.Join(cfg.RootDir, "pages")
	if _, err := os.Stat(pagesDir); os.IsNotExist(err) {
		return fmt.Errorf("pages/ directory not found in %s", cfg.RootDir)
	}
	return filepath.WalkDir(pagesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".go" {
			rel, _ := filepath.Rel(cfg.RootDir, path)
			fmt.Printf("  route: %s\n", rel)
		}
		return nil
	})
}

func compileWASM(cfg Config) error {
	outDir := filepath.Join(cfg.RootDir, cfg.OutDir)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}

	outFile := filepath.Join(outDir, "main.wasm")

	switch cfg.Target {
	case "tinygo":
		return compileTinyGo(cfg.RootDir, outFile)
	default:
		return compileStdGo(cfg.RootDir, outFile)
	}
}

func compileTinyGo(rootDir, outfile string) error {
	if _, err := exec.LookPath("tinygo"); err != nil {
		fmt.Printf("\n    ⚠ TinyGo not found — install from https://tinygo.org/getting-started/\n")
		fmt.Printf("	  writing stub WASM for now\n	")
		return os.WriteFile(outfile, []byte("(stub wasm - instll tinygo to compile)"), 0644)
	}
	cmd := exec.Command("tinygo", "build",
		"-o", outfile,
		"-target", "wasm",
		"-no-debug",
		".",
	)
	cmd.Dir = rootDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func compileStdGo(rootDir, outfile string) error {
	cmd := exec.Command("go", "build",
		"-o", outfile,
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
	html := buildHTMLShell("", "")
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

func emitBridge(cfg Config) error {
	dst := filepath.Join(cfg.RootDir, cfg.OutDir, "gowave.js")
	src := filepath.Join(cfg.RootDir, "public", "gowave.js")
	if data, err := os.ReadFile(src); err != nil {
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

const productionBridgeStub = `/* gowave.js production bridge - emitted by 'gowave build' */`


