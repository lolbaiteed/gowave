// Package ui provides the GoWave virtual DOM.
//
// ui.Node is a lightweight tree of elements that:
//   - Renders to HTML strings on the server (SSR)
//   - Compiles to WASM on the client and diffs against the live DOM
//
// All builder functions (Div, Span, Button, etc.) return a Node.
// Modifiers (Class, Text, OnClick, etc.) are applied as NodeOptions.
package ui

import (
	"fmt"
	"html"
	"strings"
)

// NodeType classifies a Node.
type NodeType uint8

const (
	ElementNode NodeType = iota
	TextNode
	FragmentNode
	RawNode // raw unescaped content for inline <script>/<style>
)

// Node is a virtual DOM node. Zero value is a valid empty fragment.
type Node struct {
	Type     NodeType
	Tag      string
	Attrs    []Attr
	Children []Node
	Text     string
	Key      string // for list reconciliation
}

// Attr is a key-value HTML attribute pair.
// HandlerID is set for event attributes (onclick, oninput).
type Attr struct {
	Key       string
	Value     string
	HandlerID string // non-empty → event binding, not a plain attribute
}

// Option is a functional modifier applied to a Node during construction.
type Option func(*Node)

// ── Element constructors ──────────────────────────────────────────────────────

func Html(opts ...Option) Node  { return el("html", opts) }
func Head(opts ...Option) Node  { return el("head", opts) }
func Body(opts ...Option) Node  { return el("body", opts) }
func Div(opts ...Option) Node   { return el("div", opts) }
func Span(opts ...Option) Node  { return el("span", opts) }
func P(opts ...Option) Node     { return el("p", opts) }
func H1(opts ...Option) Node    { return el("h1", opts) }
func H2(opts ...Option) Node    { return el("h2", opts) }
func H3(opts ...Option) Node    { return el("h3", opts) }
func Nav(opts ...Option) Node   { return el("nav", opts) }
func Header(opts ...Option) Node { return el("header", opts) }
func Footer(opts ...Option) Node { return el("footer", opts) }
func Main(opts ...Option) Node  { return el("main", opts) }
func Section(opts ...Option) Node { return el("section", opts) }
func Article(opts ...Option) Node { return el("article", opts) }
func Ul(opts ...Option) Node    { return el("ul", opts) }
func Ol(opts ...Option) Node    { return el("ol", opts) }
func Li(opts ...Option) Node    { return el("li", opts) }
func A(opts ...Option) Node     { return el("a", opts) }
func Img(opts ...Option) Node   { return el("img", opts) }
func Form(opts ...Option) Node  { return el("form", opts) }
func Label(opts ...Option) Node { return el("label", opts) }
func Input(opts ...Option) Node { return el("input", opts) }
func Textarea(opts ...Option) Node { return el("textarea", opts) }
func Select(opts ...Option) Node { return el("select", opts) }
func SelectOption(opts ...Option) Node { return el("option", opts) }
func Button(opts ...Option) Node { return el("button", opts) }
func Table(opts ...Option) Node { return el("table", opts) }
func Thead(opts ...Option) Node { return el("thead", opts) }
func Tbody(opts ...Option) Node { return el("tbody", opts) }
func Tr(opts ...Option) Node    { return el("tr", opts) }
func Th(opts ...Option) Node    { return el("th", opts) }
func Td(opts ...Option) Node    { return el("td", opts) }
func Pre(opts ...Option) Node   { return el("pre", opts) }
func Code(opts ...Option) Node  { return el("code", opts) }

// Void elements (self-closing)
func Meta(name, content string) Node {
	return el("meta", []Option{Attr2("name", name), Attr2("content", content)})
}
func Script(src string) Node {
	return el("script", []Option{Attr2("src", src), Attr2("type", "module")})
}
func Link(rel, href string) Node {
	return el("link", []Option{Attr2("rel", rel), Attr2("href", href)})
}
func Title(text string) Node {
	return el("title", []Option{Text(text)})
}

// ── Text & Fragment ───────────────────────────────────────────────────────────

// Text creates a text node.
func Text(s string) Option {
	return func(n *Node) {
		n.Children = append(n.Children, Node{Type: TextNode, Text: s})
	}
}

// Textf creates a formatted text node.
func Textf(format string, args ...any) Option {
	return Text(fmt.Sprintf(format, args...))
}

// Fragment wraps multiple nodes with no wrapping element.
func Fragment(children ...Node) Node {
	return Node{Type: FragmentNode, Children: children}
}

// Map converts a slice to a slice of Nodes.
func Map[T any](items []T, fn func(int, T) Node) []Node {
	nodes := make([]Node, len(items))
	for i, item := range items {
		nodes[i] = fn(i, item)
	}
	return nodes
}

// If conditionally returns one of two nodes.
func If(cond bool, then, otherwise Node) Node {
	if cond {
		return then
	}
	return otherwise
}

// ── Attribute options ─────────────────────────────────────────────────────────

// Class sets the CSS class.
func Class(c string) Option { return Attr2("class", c) }

// ID sets the element id.
func ID(id string) Option { return Attr2("id", id) }

// Href sets the href attribute.
func Href(href string) Option { return Attr2("href", href) }

// Src sets the src attribute.
func Src(src string) Option { return Attr2("src", src) }

// Alt sets the alt attribute.
func Alt(alt string) Option { return Attr2("alt", alt) }

// Value sets the value attribute.
func Value(v string) Option { return Attr2("value", v) }

// Placeholder sets the placeholder attribute.
func Placeholder(p string) Option { return Attr2("placeholder", p) }

// Checked sets the checked attribute.
func Checked(c bool) Option {
	return func(n *Node) {
		if c {
			n.Attrs = append(n.Attrs, Attr{Key: "checked"})
		}
	}
}

// Disabled sets the disabled attribute.
func Disabled(d bool) Option {
	return func(n *Node) {
		if d {
			n.Attrs = append(n.Attrs, Attr{Key: "disabled"})
		}
	}
}

// Attr2 adds a raw key=value attribute.
func Attr2(key, value string) Option {
	return func(n *Node) {
		n.Attrs = append(n.Attrs, Attr{Key: key, Value: value})
	}
}

// DataAttr adds a data-* attribute.
func DataAttr(key, value string) Option {
	return Attr2("data-"+key, value)
}

// KeyAttr sets the reconciliation key (used for list diffing).
func KeyAttr(k string) Option {
	return func(n *Node) { n.Key = k }
}

// ── Event options ─────────────────────────────────────────────────────────────
// On the server these emit data-gw-* attributes that the JS bridge reads.
// In WASM they register Go callbacks directly with the DOM event system.

// OnClick registers a click handler.
func OnClick(fn func()) Option {
	id := registerHandler(fn)
	return func(n *Node) {
		n.Attrs = append(n.Attrs, Attr{Key: "data-gw-click", Value: id, HandlerID: id})
	}
}

// OnInput registers an input handler that receives the current value.
func OnInput(fn func(string)) Option {
	id := registerInputHandler(fn)
	return func(n *Node) {
		n.Attrs = append(n.Attrs, Attr{Key: "data-gw-input", Value: id, HandlerID: id})
	}
}

// OnChange registers a change handler.
func OnChange(fn func()) Option {
	id := registerHandler(fn)
	return func(n *Node) {
		n.Attrs = append(n.Attrs, Attr{Key: "data-gw-change", Value: id, HandlerID: id})
	}
}

// OnEnter registers an Enter-key handler on an input.
func OnEnter(fn func()) Option {
	id := registerHandler(fn)
	return func(n *Node) {
		n.Attrs = append(n.Attrs, Attr{Key: "data-gw-enter", Value: id, HandlerID: id})
	}
}

// ── Child options ─────────────────────────────────────────────────────────────

// Child appends a child Node.
func Child(child Node) Option {
	return func(n *Node) {
		n.Children = append(n.Children, child)
	}
}

// Children appends multiple child Nodes.
func Children(children ...Node) Option {
	return func(n *Node) {
		n.Children = append(n.Children, children...)
	}
}

// ── SSR rendering ─────────────────────────────────────────────────────────────

// RenderHTML renders a Node tree to an HTML string.
// Called on the server for SSR; not used in the WASM binary.
func RenderHTML(n Node) string {
	var sb strings.Builder
	renderNode(&sb, n)
	return sb.String()
}

func renderNode(sb *strings.Builder, n Node) {
	switch n.Type {
	case RawNode:
		sb.WriteString(n.Text)
	case TextNode:
		sb.WriteString(html.EscapeString(n.Text))
	case FragmentNode:
		for _, child := range n.Children {
			renderNode(sb, child)
		}
	case ElementNode:
		sb.WriteByte('<')
		sb.WriteString(n.Tag)
		for _, attr := range n.Attrs {
			if attr.Value == "" && attr.HandlerID == "" {
				// Boolean attribute (checked, disabled)
				sb.WriteByte(' ')
				sb.WriteString(attr.Key)
				continue
			}
			sb.WriteByte(' ')
			sb.WriteString(attr.Key)
			sb.WriteString(`="`)
			sb.WriteString(html.EscapeString(attr.Value))
			sb.WriteByte('"')
		}
		if isVoid(n.Tag) {
			sb.WriteString("/>")
			return
		}
		sb.WriteByte('>')
		for _, child := range n.Children {
			renderNode(sb, child)
		}
		sb.WriteString("</")
		sb.WriteString(n.Tag)
		sb.WriteByte('>')
	}
}

var voidTags = map[string]bool{
	"area": true, "base": true, "br": true, "col": true,
	"embed": true, "hr": true, "img": true, "input": true,
	"link": true, "meta": true, "param": true, "source": true,
	"track": true, "wbr": true,
}

func isVoid(tag string) bool { return voidTags[tag] }

// ── Internal constructor ──────────────────────────────────────────────────────

func el(tag string, opts []Option) Node {
	n := Node{Type: ElementNode, Tag: tag}
	for _, opt := range opts {
		if opt != nil {
			opt(&n)
		}
	}
	return n
}

// ── Component interface ───────────────────────────────────────────────────────

// Page is the interface every GoWave page component must satisfy.
// Defined here so both the SSR renderer and the WASM runtime can use it
// without importing each other.
type Page interface {
	Render() Node
}
