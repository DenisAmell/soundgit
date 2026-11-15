// Package rpp reads a Reaper project file into a generic tree and writes it
// back without losing a byte. The tree knows nothing about the meaning of
// tags: every line keeps its original text, so unknown constructs survive.
package rpp

import (
	"bytes"
	"fmt"
	"strings"
)

// Token is one whitespace-separated value on a line. Quote is the quote
// character it was written with (", ' or `), or 0 for a bare token.
type Token struct {
	Text  string
	Quote byte
}

// Node is a line or a block. A block starts with "<TAG args", holds child
// nodes and ends with a ">" line.
type Node struct {
	Raw      string // original line text without the line ending
	EOL      string // "\n", "\r\n" or "" for a last line without a newline
	Tokens   []Token
	Block    bool
	Children []*Node
	CloseRaw string // original text of the closing ">" line
	CloseEOL string
	Line     int // 1-based line number in the source file

	dirty bool // tokens were changed; Raw is stale
}

// File is a parsed project file. Nodes holds the top-level sequence; Root is
// the REAPER_PROJECT block among them.
type File struct {
	Nodes []*Node
	Root  *Node
	BOM   bool
	EOL   string // dominant line ending, used for newly formatted lines
}

// ParseError reports a structural problem with the line it was found on.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

var bom = []byte{0xEF, 0xBB, 0xBF}

// Parse builds the tree. It fails on unbalanced blocks and on input that is
// not a Reaper project; it never returns a partial tree.
func Parse(data []byte) (*File, error) {
	f := &File{EOL: "\n"}
	if bytes.HasPrefix(data, bom) {
		f.BOM = true
		data = data[len(bom):]
	}

	var stack []*Node
	crlf, lf := 0, 0
	lineNo := 0
	for len(data) > 0 {
		lineNo++
		var raw, eol string
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			raw, eol = string(data[:i]), "\n"
			data = data[i+1:]
			if strings.HasSuffix(raw, "\r") {
				raw, eol = raw[:len(raw)-1], "\r\n"
				crlf++
			} else {
				lf++
			}
		} else {
			raw = string(data)
			data = nil
		}

		trimmed := strings.TrimLeft(raw, " \t")
		if strings.TrimRight(trimmed, " \t") == ">" {
			if len(stack) == 0 {
				return nil, &ParseError{lineNo, "closing '>' without an open block"}
			}
			top := stack[len(stack)-1]
			top.CloseRaw, top.CloseEOL = raw, eol
			stack = stack[:len(stack)-1]
			continue
		}

		n := &Node{Raw: raw, EOL: eol, Line: lineNo}
		if strings.HasPrefix(trimmed, "<") {
			n.Block = true
			n.Tokens = Tokenize(trimmed[1:])
		} else {
			n.Tokens = Tokenize(trimmed)
		}
		if len(stack) == 0 {
			f.Nodes = append(f.Nodes, n)
		} else {
			top := stack[len(stack)-1]
			top.Children = append(top.Children, n)
		}
		if n.Block {
			stack = append(stack, n)
		}
	}
	if len(stack) > 0 {
		open := stack[len(stack)-1]
		return nil, &ParseError{open.Line, fmt.Sprintf("block <%s is never closed", open.Tag())}
	}
	if crlf > lf {
		f.EOL = "\r\n"
	}

	for _, n := range f.Nodes {
		if len(n.Tokens) == 0 {
			continue // blank line
		}
		if n.Block && n.Tag() == "REAPER_PROJECT" {
			f.Root = n
		}
		break
	}
	if f.Root == nil {
		return nil, &ParseError{1, "not a Reaper project: the file does not start with <REAPER_PROJECT"}
	}
	return f, nil
}

// Tag is the first token: the block tag or the attribute name.
func (n *Node) Tag() string {
	if len(n.Tokens) == 0 {
		return ""
	}
	return n.Tokens[0].Text
}

// Arg returns the i-th value after the tag, or "" when absent.
func (n *Node) Arg(i int) string {
	if i+1 < len(n.Tokens) {
		return n.Tokens[i+1].Text
	}
	return ""
}

// SetTokens replaces the tokens; the line is reformatted on write.
func (n *Node) SetTokens(t []Token) {
	n.Tokens = t
	n.dirty = true
}

// Text is the line as it will be written, without indentation or line ending.
func (n *Node) Text() string {
	if !n.dirty {
		return strings.TrimLeft(n.Raw, " \t")
	}
	s := FormatTokens(n.Tokens)
	if n.Block {
		return "<" + s
	}
	return s
}

// Bytes writes the tree. Untouched lines are emitted exactly as read.
func (f *File) Bytes() []byte {
	var b bytes.Buffer
	if f.BOM {
		b.Write(bom)
	}
	for _, n := range f.Nodes {
		f.write(&b, n, 0)
	}
	return b.Bytes()
}

func (f *File) write(b *bytes.Buffer, n *Node, depth int) {
	if n.dirty {
		b.WriteString(strings.Repeat("  ", depth))
		b.WriteString(n.Text())
		b.WriteString(f.eol(n.EOL, n.Raw == ""))
	} else {
		b.WriteString(n.Raw)
		b.WriteString(n.EOL)
	}
	if !n.Block {
		return
	}
	for _, c := range n.Children {
		f.write(b, c, depth+1)
	}
	if n.CloseRaw == "" {
		b.WriteString(strings.Repeat("  ", depth) + ">" + f.EOL)
		return
	}
	b.WriteString(n.CloseRaw)
	b.WriteString(n.CloseEOL)
}

func (f *File) eol(orig string, synthesized bool) string {
	if synthesized {
		return f.EOL
	}
	return orig
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' }

func isQuote(c byte) bool { return c == '"' || c == '\'' || c == '`' }

// Tokenize splits a line into tokens. It never fails: an unterminated quote
// takes the rest of the line.
func Tokenize(s string) []Token {
	var out []Token
	i := 0
	for i < len(s) {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		if i >= len(s) {
			break
		}
		if q := s[i]; isQuote(q) {
			end := strings.IndexByte(s[i+1:], q)
			if end < 0 {
				out = append(out, Token{Text: s[i+1:], Quote: q})
				break
			}
			out = append(out, Token{Text: s[i+1 : i+1+end], Quote: q})
			i += end + 2
			continue
		}
		start := i
		for i < len(s) && !isSpace(s[i]) {
			i++
		}
		out = append(out, Token{Text: s[start:i]})
	}
	return out
}

// FormatToken renders a token, keeping its original quote style when it is
// still valid for the text and choosing a safe one otherwise.
func FormatToken(t Token) string {
	if t.Quote != 0 && strings.IndexByte(t.Text, t.Quote) < 0 {
		return string(t.Quote) + t.Text + string(t.Quote)
	}
	needs := t.Text == "" || strings.ContainsAny(t.Text, " \t") || isQuote(t.Text[0])
	if !needs && t.Quote == 0 {
		return t.Text
	}
	for _, q := range []byte{'"', '\'', '`'} {
		if strings.IndexByte(t.Text, q) < 0 {
			return string(q) + t.Text + string(q)
		}
	}
	// All three quote characters are present: Reaper degrades backticks.
	return "`" + strings.ReplaceAll(t.Text, "`", "'") + "`"
}

// FormatTokens renders a whole line.
func FormatTokens(ts []Token) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = FormatToken(t)
	}
	return strings.Join(parts, " ")
}
