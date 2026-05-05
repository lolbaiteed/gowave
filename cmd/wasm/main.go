package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/lolbaiteed/gowave/internal/builder"
	"github.com/lolbaiteed/gowave/internal/devserver"
	"github.com/lolbaiteed/gowave/internal/parser"
	"github.com/lolbaiteed/gowave/internal/scaffold"
)

const version = "0.1.0"

// gowaveSrcPath is injected at build time via:
//
//	go build -ldflags "-X main.gowaveSrcPath=$(pwd)" ./cmd/gowave
//
// When set, it tells gowave new exactly where the framework source lives
// so the generated go.mod gets the right replace directive automatically.
var gowaveSrcPath string

const banner = `
  __ _  _____      ____ ___   _____
 / _` + "`" + `  |/ _ \ \ /\ / / _` + "`" + ` \ \ / / _ \
| (_| | (_) \ V  V / (_| |\ V /  __/
 \__, |\___/ \_/\_/ \__,_| \_/ \___|
  __/ |
 |___/   v` + version + `  — Go + WASM, no JS tax.
`

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(0)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "new":
		runNew(args)
	case "dev":
		runDev(args)
	case "build":
		runBuild(args)
	case "routes":
		runRoutes(args)
	case "version", "--version", "-v":
		fmt.Printf("gowave v%s\n", version)
	case "help", "--help", "-h":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\nRun 'gowave help' for usage.\n", cmd)
		os.Exit(1)
	}
}

func runNew(args []string) {
	if len(args) == 0 {
		fatalf("usage: gowave new <project-name>\n")
	}
	name := args[0]
	gowavePath := detectGowavePath()

	opts := scaffold.Options{
		Name:   name,
		Dir:    name,
		Module: "github.com/you/" + name,
	}
	for i := 1; i < len(args)-1; i++ {
		switch args[i] {
		case "--module":
			opts.Module = args[i+1]
		case "--dir":
			opts.Dir = args[i+1]
		case "--gowave":
			gowavePath = args[i+1]
		}
	}

	opts.GowavePath = gowavePath
	opts.GowaveModule = resolveGowaveModule(gowavePath)

	if err := scaffold.Run(opts); err != nil {
		fatalf("scaffold failed: %v\n", err)
	}
}

// detectGowavePath finds the gowave source root automatically.
// Priority:
//  1. gowaveSrcPath injected at build time via -ldflags (most reliable)
//  2. Go build info module path + filesystem walk
//  3. Walk up from binary / working directory by directory shape
// It uses Go build info embedded in the binary to find the exact module path,
// then walks the filesystem to find where it lives. Falls back to walking up
// from the binary or checking the working directory.
func detectGowavePath() string {
	// 1. Injected at build time — always correct when using the Makefile/install script
	if gowaveSrcPath != "" && isGowaveRoot(gowaveSrcPath) {
		return gowaveSrcPath
	}

	// 2. Build info embeds the module path of the binary (e.g. "github.com/lolbaiteed/gowave").
	// We use this to find the source root without any hardcoded module names.
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Path != "" {
		exe, _ := os.Executable()
		if dir := walkForGoMod(filepath.Dir(exe), info.Main.Path); dir != "" {
			return dir
		}
		if wd, err := os.Getwd(); err == nil {
			if dir := walkForGoMod(wd, info.Main.Path); dir != "" {
				return dir
			}
		}
	}
	// Fallback: walk up from binary looking for a gowave-shaped directory.
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exe)
		for i := 0; i < 8; i++ {
			if isGowaveRoot(dir) {
				return dir
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	// Fallback: working directory (covers go run ./cmd/gowave).
	if wd, err := os.Getwd(); err == nil && isGowaveRoot(wd) {
		return wd
	}
	return ""
}

// walkForGoMod searches dir and its parents for a go.mod declaring modulePath.
func walkForGoMod(startDir, modulePath string) string {
	dir := startDir
	for i := 0; i < 8; i++ {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.TrimSpace(line) == "module "+modulePath {
					return dir
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// isGowaveRoot returns true if dir looks like a gowave source tree.
func isGowaveRoot(dir string) bool {
	for _, m := range []string{"go.mod", filepath.Join("pkg", "ui"), filepath.Join("internal", "ssr")} {
		if _, err := os.Stat(filepath.Join(dir, m)); err != nil {
			return false
		}
	}
	return true
}

// resolveGowaveModule reads the module declaration from the gowave source go.mod.
// This correctly handles any fork or renamed module path.
func resolveGowaveModule(gowavePath string) string {
	if gowavePath == "" {
		return "github.com/gowave/gowave"
	}
	data, err := os.ReadFile(filepath.Join(gowavePath, "go.mod"))
	if err != nil {
		return "github.com/gowave/gowave"
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return "github.com/gowave/gowave"
}

func runDev(args []string) {
	port := "3000"
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--port" || args[i] == "-p" {
			port = args[i+1]
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		fatalf("getting working directory: %v\n", err)
	}
	cfg := devserver.Config{
		Port:    port,
		RootDir: wd,
	}
	if err := devserver.Run(cfg); err != nil {
		fatalf("dev server failed: %v\n", err)
	}
}

func runBuild(args []string) {
	outDir := "dist"
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--out" || args[i] == "-o" {
			outDir = args[i+1]
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		fatalf("getting working directory: %v\n", err)
	}
	cfg := builder.Config{
		RootDir: wd,
		OutDir:  outDir,
	}
	if err := builder.Run(cfg); err != nil {
		fatalf("build failed: %v\n", err)
	}
}

func runRoutes(args []string) {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	res, err := parser.ParsePages(dir)
	if err != nil {
		fatalf("parse failed: %v\n", err)
	}
	m := &parser.Manifest{Routes: res.Routes, Warnings: res.Warnings}
	fmt.Printf("\n  routes in %s/pages/\n\n", dir)
	m.Print()
	fmt.Println()
}

func printHelp() {
	fmt.Print(banner)
	fmt.Print(`
Usage:
  gowave new <n>          Scaffold a new project
  gowave dev              Start dev server with hot reload
  gowave build            Compile to WASM + SSR bundle
  gowave routes [dir]     Print discovered routes (default: .)
  gowave version          Show version

Flags (new):
  --module <path>         Go module path  (default: github.com/you/<n>)
  --dir    <path>         Output directory (default: <n>)
  --gowave <path>         Path to gowave source directory

Flags (dev):
  --port, -p <port>       Port to listen on (default: 3000)

Flags (build):
  --out, -o <dir>         Output directory (default: dist/)

`)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format, args...)
	os.Exit(1)
}
