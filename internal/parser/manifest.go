package parser

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Manifest is the full route table for a GoWave project.
// It is written to .gowave-cache/routes.json and consumed by the
// dev server, SSR renderer, and WASM code-generator.
type Manifest struct {
	Routes   []*Route `json:"routes"`
	Warnings []string `json:"warnings,omitempty"`
}

// WriteManifest serialises the manifest to outDir/routes.json.
func WriteManifest(m *Manifest, outDir string) error {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "routes.json"), data, 0644)
}

// ReadManifest loads a previously written manifest.
func ReadManifest(outDir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(outDir, "routes.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	return &m, json.Unmarshal(data, &m)
}

// Print writes a human-readable summary of the manifest to stdout.
func (m *Manifest) Print() {
	if len(m.Routes) == 0 {
		fmt.Println("  no routes found")
		return
	}

	// Sort by HTTP path for stable output
	sorted := make([]*Route, len(m.Routes))
	copy(sorted, m.Routes)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].HTTPPath < sorted[j].HTTPPath
	})

	// Column widths
	maxPath := 4 // "path"
	maxStruct := 6 // "struct"
	for _, r := range sorted {
		if len(r.HTTPPath) > maxPath {
			maxPath = len(r.HTTPPath)
		}
		if len(r.StructName) > maxStruct {
			maxStruct = len(r.StructName)
		}
	}

	fmt.Printf("  %-*s  %-*s  flags\n", maxPath, "path", maxStruct, "struct")
	fmt.Printf("  %s  %s  -----\n",
		strings.Repeat("-", maxPath),
		strings.Repeat("-", maxStruct))

	for _, r := range sorted {
		flags := buildFlags(r)
		fmt.Printf("  %-*s  %-*s  %s\n",
			maxPath, r.HTTPPath,
			maxStruct, r.StructName,
			flags)
	}

	if len(m.Warnings) > 0 {
		fmt.Println()
		for _, w := range m.Warnings {
			fmt.Printf("  warn: %s\n", w)
		}
	}
}

func buildFlags(r *Route) string {
	var parts []string
	if r.HasLoader {
		parts = append(parts, "loader")
	}
	if r.HasSSR {
		parts = append(parts, "ssr")
	}
	if len(r.Actions) > 0 {
		parts = append(parts, fmt.Sprintf("%d action(s)", len(r.Actions)))
	}
	if len(r.Middlewares) > 0 {
		parts = append(parts, fmt.Sprintf("mw:%s", strings.Join(r.Middlewares, ",")))
	}
	if r.Layout != "" {
		parts = append(parts, "layout:"+r.Layout)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "  ")
}
