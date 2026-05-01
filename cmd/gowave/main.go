package main

import (
	"fmt"
	"os"

	"github.com/lolbaiteed/gowave/internal/builder"
	"github.com/lolbaiteed/gowave/internal/devserver"
	"github.com/lolbaiteed/gowave/internal/parser"
	"github.com/lolbaiteed/gowave/internal/scaffold"
)

const version = "0.1.0"

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
		}
	}
	if err := scaffold.Run(opts); err != nil {
		fatalf("scaffold failed: %v\n", err)
	}
}

func runDev(args []string) {
	port := "3000"
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--port" || args[i] == "-p" {
			port = args[i+1]
		}
	}
	cfg := devserver.Config{
		Port:    port,
		RootDir: ".",
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
	cfg := builder.Config{
		RootDir: ".",
		OutDir:  outDir,
		Target:  "tinygo",
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
