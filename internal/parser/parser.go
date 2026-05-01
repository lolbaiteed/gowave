// Package parser scans a GoWave project's pages/ directory and extracts
// route definitions by reading Go source files with the AST.
//
// A route is defined by a struct annotated with:
//
//	// +gowave:page route="/path"
//	type MyPage struct { ... }
//
// Optional additional annotations on the same comment block:
//
//	// +gowave:page route="/blog/[slug]"
//	// +gowave:middleware auth.Required
//	// +gowave:layout blog
package parser

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// ─── Public types ────────────────────────────────────────────────────────────

// Route describes a single page route discovered in pages/.
type Route struct {
	// HTTPPath is the URL pattern, e.g. "/" or "/blog/:slug".
	// Dynamic segments like [slug] are normalised to :slug.
	HTTPPath string

	// RawPattern is the value in the +gowave:page annotation, e.g. "/blog/[slug]".
	RawPattern string

	// StructName is the Go type name, e.g. "IndexPage".
	StructName string

	// Package is the Go package name declared in the file, e.g. "pages".
	Package string

	// FilePath is the absolute path to the source file.
	FilePath string

	// RelPath is the path relative to pages/, e.g. "blog/[slug].go".
	RelPath string

	// Middlewares lists any +gowave:middleware values.
	Middlewares []string

	// Layout is the named layout (empty = root layout).
	Layout string

	// HasLoader is true when the struct has a Load(ctx) method.
	HasLoader bool

	// HasSSR is true when the struct has a ServerRender() method.
	HasSSR bool

	// Actions lists methods annotated with +gowave:action.
	Actions []ActionDef

	// Fields lists exported struct fields (used for state serialisation).
	Fields []FieldDef
}

// ActionDef describes a server action method.
type ActionDef struct {
	Name       string
	TakesCtx   bool   // first param is context.Context
	ReturnsErr bool   // returns error
}

// FieldDef describes an exported field on the page struct.
type FieldDef struct {
	Name    string
	GoType  string
	JSONKey string // snake_case of Name
}

// ParseResult holds all routes discovered in a project.
type ParseResult struct {
	Routes   []*Route
	Warnings []string
}

// ─── Entry point ─────────────────────────────────────────────────────────────

// ParsePages walks rootDir/pages/ and returns all discovered routes.
func ParsePages(rootDir string) (*ParseResult, error) {
	pagesDir := filepath.Join(rootDir, "pages")
	if _, err := os.Stat(pagesDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("pages/ directory not found in %s", rootDir)
	}

	result := &ParseResult{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(pagesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		relPath, _ := filepath.Rel(pagesDir, path)
		routes, warnings, err := parseFile(fset, path, relPath)
		if err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("%s: parse error: %v", relPath, err))
			return nil // soft-fail per file
		}

		result.Routes = append(result.Routes, routes...)
		result.Warnings = append(result.Warnings, warnings...)
		return nil
	})

	return result, err
}

// ─── File-level parser ────────────────────────────────────────────────────────

func parseFile(fset *token.FileSet, absPath, relPath string) ([]*Route, []string, error) {
	src, err := os.ReadFile(absPath)
	if err != nil {
		return nil, nil, err
	}

	f, err := parser.ParseFile(fset, absPath, src, parser.ParseComments)
	if err != nil {
		return nil, nil, fmt.Errorf("syntax error: %w", err)
	}

	pkgName := f.Name.Name
	var routes []*Route
	var warnings []string

	// Build a map: struct name → its +gowave:page annotation block
	annotated := extractAnnotatedStructs(f)

	// For each annotated struct, build a Route
	for structName, ann := range annotated {
		pageAnn, ok := ann.directives["page"]
		if !ok {
			continue
		}

		rawPattern := extractAttr(pageAnn, "route")
		if rawPattern == "" {
			// Fall back to inferring the route from the file path
			rawPattern = inferPattern(relPath)
			warnings = append(warnings,
				fmt.Sprintf("%s: %s has no route= attribute; inferred %q",
					relPath, string(structName), rawPattern))
		}

		route := &Route{
			RawPattern: rawPattern,
			HTTPPath:   normalisePattern(rawPattern),
			StructName: structName,
			Package:    pkgName,
			FilePath:   absPath,
			RelPath:    relPath,
			Layout:     extractAttr(ann.directives["layout"], ""),
		}

		// Middlewares: each +gowave:middleware line adds one entry
		for _, mw := range ann.middlewareLines {
			route.Middlewares = append(route.Middlewares, strings.TrimSpace(mw))
		}

		// Collect struct fields
		route.Fields = extractFields(f, structName)

		// Collect methods: loaders, actions
		methods := extractMethods(f, structName)
		for _, m := range methods {
			switch {
			case m.name == "Load":
				route.HasLoader = true
			case m.name == "ServerRender":
				route.HasSSR = true
			case m.hasDirective("action"):
				route.Actions = append(route.Actions, ActionDef{
					Name:       m.name,
					TakesCtx:   m.takesCtx,
					ReturnsErr: m.returnsErr,
				})
			}
		}

		routes = append(routes, route)
	}

	return routes, warnings, nil
}

// ─── Annotation extraction ────────────────────────────────────────────────────

type annotationBlock struct {
	directives      map[string]string // directive name → raw value string
	middlewareLines []string
}

type structAnnotation struct {
	directives      map[string]string
	middlewareLines []string
}

// extractAnnotatedStructs returns, for each struct declaration preceded
// by a comment block containing +gowave:* directives, the struct name
// and parsed directives.
func extractAnnotatedStructs(f *ast.File) map[string]*structAnnotation {
	result := make(map[string]*structAnnotation)

	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE || genDecl.Doc == nil {
			continue
		}

		ann := parseCommentBlock(genDecl.Doc)
		if len(ann.directives) == 0 && len(ann.middlewareLines) == 0 {
			continue
		}

		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, ok := typeSpec.Type.(*ast.StructType); !ok {
				continue
			}
			result[typeSpec.Name.Name] = ann
		}
	}

	return result
}

// parseCommentBlock extracts +gowave:* directives from a *ast.CommentGroup.
func parseCommentBlock(doc *ast.CommentGroup) *structAnnotation {
	ann := &structAnnotation{directives: make(map[string]string)}

	for _, comment := range doc.List {
		text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
		if !strings.HasPrefix(text, "+gowave:") {
			continue
		}
		text = strings.TrimPrefix(text, "+gowave:")

		// Split into directive name and optional value
		parts := strings.SplitN(text, " ", 2)
		name := parts[0]
		value := ""
		if len(parts) == 2 {
			value = strings.TrimSpace(parts[1])
		}

		if name == "middleware" {
			ann.middlewareLines = append(ann.middlewareLines, value)
		} else {
			ann.directives[name] = value
		}
	}

	return ann
}

// extractAttr parses key="value" pairs from an annotation value string.
// If key is empty, returns the entire value trimmed.
func extractAttr(raw, key string) string {
	if key == "" {
		return strings.Trim(raw, `"`)
	}
	prefix := key + `="`
	idx := strings.Index(raw, prefix)
	if idx == -1 {
		// Try without quotes: key=value
		prefix2 := key + "="
		idx2 := strings.Index(raw, prefix2)
		if idx2 == -1 {
			return ""
		}
		rest := raw[idx2+len(prefix2):]
		end := strings.IndexAny(rest, " \t")
		if end == -1 {
			return rest
		}
		return rest[:end]
	}
	rest := raw[idx+len(prefix):]
	end := strings.Index(rest, `"`)
	if end == -1 {
		return rest
	}
	return rest[:end]
}

// ─── Method extraction ────────────────────────────────────────────────────────

type methodInfo struct {
	name        string
	directives  map[string]bool
	takesCtx    bool
	returnsErr  bool
}

func (m methodInfo) hasDirective(d string) bool {
	return m.directives[d]
}

// extractMethods returns all methods whose receiver is *structName or structName.
func extractMethods(f *ast.File, structName string) []methodInfo {
	var methods []methodInfo

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		if !receiverMatches(fn.Recv.List[0].Type, structName) {
			continue
		}

		info := methodInfo{
			name:       fn.Name.Name,
			directives: make(map[string]bool),
		}

		// Parse comment directives
		if fn.Doc != nil {
			for _, c := range fn.Doc.List {
				text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
				if strings.HasPrefix(text, "+gowave:") {
					dir := strings.TrimPrefix(text, "+gowave:")
					dir = strings.Fields(dir)[0]
					info.directives[dir] = true
				}
			}
		}

		// Check if first param is context.Context
		if fn.Type.Params != nil {
			for _, param := range fn.Type.Params.List {
				if isContextType(param.Type) {
					info.takesCtx = true
				}
			}
		}

		// Check if returns error
		if fn.Type.Results != nil {
			for _, result := range fn.Type.Results.List {
				if isErrorType(result.Type) {
					info.returnsErr = true
				}
			}
		}

		methods = append(methods, info)
	}

	return methods
}

func receiverMatches(expr ast.Expr, name string) bool {
	switch t := expr.(type) {
	case *ast.StarExpr:
		if ident, ok := t.X.(*ast.Ident); ok {
			return ident.Name == name
		}
	case *ast.Ident:
		return t.Name == name
	}
	return false
}

func isContextType(expr ast.Expr) bool {
	// Matches context.Context
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "context" && sel.Sel.Name == "Context"
}

func isErrorType(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "error"
}

// ─── Field extraction ─────────────────────────────────────────────────────────

// extractFields returns the exported fields of structName.
func extractFields(f *ast.File, structName string) []FieldDef {
	var fields []FieldDef

	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != structName {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range structType.Fields.List {
				for _, name := range field.Names {
					if !name.IsExported() {
						continue
					}
					fields = append(fields, FieldDef{
						Name:    name.Name,
						GoType:  typeString(field.Type),
						JSONKey: toSnakeCase(name.Name),
					})
				}
			}
		}
	}

	return fields
}

// typeString converts an ast.Expr to a readable Go type string.
func typeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeString(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + typeString(t.Elt)
		}
		return "[...]" + typeString(t.Elt)
	case *ast.SelectorExpr:
		return typeString(t.X) + "." + t.Sel.Name
	case *ast.MapType:
		return "map[" + typeString(t.Key) + "]" + typeString(t.Value)
	default:
		return "any"
	}
}

// ─── Pattern normalisation ────────────────────────────────────────────────────

// normalisePattern converts a GoWave pattern to an httprouter-style pattern.
// "/blog/[slug]" → "/blog/:slug"
// "/blog/[...rest]" → "/blog/*rest"
func normalisePattern(raw string) string {
	// Replace [param] → :param
	result := strings.NewReplacer(
		"[...", "*", // must come first to catch [...rest]
	).Replace(raw)

	// Now handle remaining [param] → :param
	for strings.Contains(result, "[") {
		start := strings.Index(result, "[")
		end := strings.Index(result, "]")
		if end == -1 || end < start {
			break
		}
		param := result[start+1 : end]
		result = result[:start] + ":" + param + result[end+1:]
	}

	// Clean up *rest] (leftover ] from [...rest])
	result = strings.ReplaceAll(result, "]", "")

	return result
}

// inferPattern infers a route pattern from the file's relative path in pages/.
// "index.go"          → "/"
// "about.go"          → "/about"
// "blog/[slug].go"    → "/blog/[slug]"
// "blog/index.go"     → "/blog"
func inferPattern(relPath string) string {
	// Strip .go extension and normalise separators
	p := strings.TrimSuffix(filepath.ToSlash(relPath), ".go")

	// index at any level → parent path
	if p == "index" {
		return "/"
	}
	parts := strings.Split(p, "/")
	if parts[len(parts)-1] == "index" {
		parts = parts[:len(parts)-1]
	}

	return "/" + strings.Join(parts, "/")
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// toSnakeCase converts "MyField" → "my_field".
func toSnakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + 32) // toLower
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
