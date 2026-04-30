// Package ssr renders GoWave page components to HTML on the server.
//
// The renderer:
//  1. Loads the route manifest produced by the parser
//  2. Matches an incoming URL path to a Route
//  3. Instantiates the page struct via reflection
//  4. Calls Render() to get a ui.Node tree
//  5. Wraps it in the root layout shell
//  6. Returns a complete HTML document
//
// This is the only Go code that needs to know about reflection — the rest
// of GoWave is plain Go with no magic.
package ssr

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/lolbaiteed/gowave/pkg/ui"
)

// Page is an alias for ui.Page — every GoWave page struct must satisfy it.
type Page = ui.Page

// Layout wraps a page's output in an HTML shell.
type Layout interface {
	Render(slot ui.Node) ui.Node
}

// Route describes a single matched route — the URL pattern plus the
// factory function that creates a fresh page instance.
type Route struct {
	// Pattern is the normalised HTTP path, e.g. "/" or "/blog/:slug".
	Pattern string

	// Params lists the dynamic segment names in order, e.g. ["slug"].
	Params []string

	// re is the compiled regex for this pattern.
	re *regexp.Regexp

	// Factory creates a fresh zero-value page for each request.
	Factory func() Page
}

// Router holds the registered routes and the root layout.
type Router struct {
	routes []*Route
	layout Layout
}

// NewRouter creates an empty Router.
func NewRouter(layout Layout) *Router {
	return &Router{layout: layout}
}

// Register adds a route with the given URL pattern and page factory.
// Pattern uses :param for dynamic segments and *param for catch-all.
func (r *Router) Register(pattern string, factory func() Page) {
	route := &Route{
		Pattern: pattern,
		Factory: factory,
	}
	route.re, route.Params = compilePattern(pattern)
	r.routes = append(r.routes, route)
}

// Match finds the first route whose pattern matches path.
// Returns the route and a map of extracted path parameters.
// Returns nil, nil if no route matches.
func (r *Router) Match(path string) (*Route, map[string]string) {
	for _, route := range r.routes {
		if params, ok := matchRoute(route, path); ok {
			return route, params
		}
	}
	return nil, nil
}

// ServeHTTP renders the matched route for the request.
// If no route matches it writes a 404.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	route, params := r.Match(req.URL.Path)
	if route == nil {
		r.serve404(w, req)
		return
	}

	page := route.Factory()

	// Inject path params into the page struct if it has matching fields.
	injectParams(page, params)

	// Render the page component.
	ui.ClearHandlers()
	content := page.Render()

	// Wrap in layout.
	var doc ui.Node
	if r.layout != nil {
		doc = r.layout.Render(content)
	} else {
		doc = defaultShell(content)
	}

	html := "<!DOCTYPE html>\n" + ui.RenderHTML(doc)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, html)
}

func (r *Router) serve404(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	fmt.Fprintf(w, "<!DOCTYPE html><html><body><h1>404 — not found</h1><p>No route matched %s</p></body></html>",
		req.URL.Path)
}

// RenderToString renders a page to a complete HTML string.
// Used by the build pipeline for static pre-rendering.
func RenderToString(page Page, layout Layout, params map[string]string) string {
	injectParams(page, params)
	ui.ClearHandlers()
	content := page.Render()

	var doc ui.Node
	if layout != nil {
		doc = layout.Render(content)
	} else {
		doc = defaultShell(content)
	}
	return "<!DOCTYPE html>\n" + ui.RenderHTML(doc)
}

// ── Pattern compilation ───────────────────────────────────────────────────────

// compilePattern converts a GoWave URL pattern to a *regexp.Regexp.
// "/blog/:slug"   → ^/blog/(?P<slug>[^/]+)$
// "/docs/*path"   → ^/docs/(?P<path>.+)$
func compilePattern(pattern string) (*regexp.Regexp, []string) {
	var params []string
	var sb strings.Builder
	sb.WriteString("^")

	segments := strings.Split(pattern, "/")
	for i, seg := range segments {
		if i > 0 {
			sb.WriteString("/")
		}
		switch {
		case strings.HasPrefix(seg, "*"):
			name := seg[1:]
			params = append(params, name)
			sb.WriteString(fmt.Sprintf("(?P<%s>.+)", regexp.QuoteMeta(name)))
		case strings.HasPrefix(seg, ":"):
			name := seg[1:]
			params = append(params, name)
			sb.WriteString(fmt.Sprintf("(?P<%s>[^/]+)", regexp.QuoteMeta(name)))
		default:
			sb.WriteString(regexp.QuoteMeta(seg))
		}
	}
	sb.WriteString("$")

	re := regexp.MustCompile(sb.String())
	return re, params
}

// matchRoute tests path against route's regex and extracts param values.
func matchRoute(route *Route, path string) (map[string]string, bool) {
	m := route.re.FindStringSubmatch(path)
	if m == nil {
		return nil, false
	}
	params := make(map[string]string, len(route.Params))
	for _, name := range route.Params {
		// FindStringSubmatch index 0 is full match, named groups follow
		idx := route.re.SubexpIndex(name)
		if idx >= 0 && idx < len(m) {
			params[name] = m[idx]
		}
	}
	return params, true
}

// ── Param injection ───────────────────────────────────────────────────────────

// injectParams sets exported string fields on the page whose names match
// param keys (case-insensitive). Uses reflection-free approach: pages
// implement an optional ParamSetter interface for typed injection.
func injectParams(page Page, params map[string]string) {
	if len(params) == 0 {
		return
	}
	// If the page opts into typed param injection, use that.
	if ps, ok := page.(ParamSetter); ok {
		ps.SetParams(params)
		return
	}
	// Otherwise fall back to reflect-based field injection.
	injectParamsReflect(page, params)
}

// ParamSetter is implemented by pages that want typed param injection
// without reflection. Optional but preferred for performance.
//
//	func (p *BlogPost) SetParams(params map[string]string) {
//	    p.Slug = params["slug"]
//	}
type ParamSetter interface {
	SetParams(map[string]string)
}

// ── Default layout shell ──────────────────────────────────────────────────────

// defaultShell wraps content in a minimal HTML document when no layout
// is registered. Used by tests and the dev server before layout.go compiles.
func defaultShell(content ui.Node) ui.Node {
	return ui.Html(
		ui.Child(ui.Head(
			ui.Child(ui.Meta("charset", "utf-8")),
			ui.Child(ui.Meta("viewport", "width=device-width, initial-scale=1")),
			ui.Child(ui.Title("GoWave")),
			ui.Child(inlineStyle(devCSS)),
		)),
		ui.Child(ui.Body(
			ui.ID("app-root"),
			ui.Child(content),
			ui.Child(rawScript(`/wasm_exec.js`)),
			ui.Child(rawScript(`/gowave.js`)),
			ui.Child(inlineScript(hotReloadScript)),
		)),
	)
}

// rawScript produces <script src="..."></script>
func rawScript(src string) ui.Node {
	return ui.Node{
		Type: ui.ElementNode,
		Tag:  "script",
		Attrs: []ui.Attr{
			{Key: "src", Value: src},
		},
	}
}

// inlineScript produces <script>...</script> with raw JS content.
func inlineScript(js string) ui.Node {
	return ui.Node{
		Type:     ui.ElementNode,
		Tag:      "script",
		Children: []ui.Node{{Type: ui.RawNode, Text: js}},
	}
}

// inlineStyle produces <style>...</style> with raw CSS.
func inlineStyle(css string) ui.Node {
	return ui.Node{
		Type:     ui.ElementNode,
		Tag:      "style",
		Children: []ui.Node{{Type: ui.RawNode, Text: css}},
	}
}

const hotReloadScript = `
(function() {
  var es = new EventSource('/_gowave/reload');
  es.addEventListener('reload', function() {
    console.log('[gowave] reloading...');
    location.reload();
  });
})();
`

const devCSS = `
*, *::before, *::after { box-sizing: border-box; }
body { font-family: system-ui, sans-serif; margin: 0; padding: 0; color: #1a1a1a; }
#app-root { min-height: 100vh; }
.container { max-width: 800px; margin: 0 auto; padding: 2rem 1rem; }
h1 { font-size: 2rem; font-weight: 700; margin-bottom: 1rem; }
p { line-height: 1.6; margin-bottom: 1rem; color: #444; }
button {
  font-family: inherit; font-size: 14px; padding: 8px 16px;
  border: 1px solid #ddd; border-radius: 6px; background: #fff;
  cursor: pointer; transition: background .15s;
}
button:hover { background: #f5f5f5; }
input, textarea {
  font-family: inherit; font-size: 14px; padding: 8px 10px;
  border: 1px solid #ddd; border-radius: 6px; width: 100%;
}
`
