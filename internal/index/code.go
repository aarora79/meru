// This file chunks source code. Go files split by top-level declaration,
// found with the standard library's own Go parser; other languages split by
// blank-line blocks, like plain text.

package index

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/aarora79/meru/internal/store"
)

// chunkCode chunks source code. A .go file that parses gets one chunk per
// top-level declaration (more when a declaration passes the size limit);
// anything else, including a Go file with a syntax error, falls back to
// blank-line blocks with line numbers.
func chunkCode(name, src string, lim limits) []store.Chunk {
	if strings.EqualFold(filepath.Ext(name), ".go") {
		if chunks, err := chunkGo(src, lim); err == nil {
			return chunks
		}
	}
	return chunkText(src, lim)
}

// maxHeadingNames caps how many names a grouped declaration such as
// const ( A = 1; B = 2; ... ) lists in its heading.
const maxHeadingNames = 5

// chunkGo parses src as Go and makes each top-level declaration (function,
// method, type, var, const or import block) its own chunk, doc comment
// included, with the declaration's name as Heading: "Scan" for a function,
// "Indexer.Scan" for a method, "Indexer" for a type. Text between
// declarations, such as the package clause and its doc comment, becomes a
// chunk of its own. A declaration longer than the limit splits at blank
// lines, and every piece keeps the name.
//
// It fails when src isn't valid Go.
func chunkGo(src string, lim limits) ([]store.Chunk, error) {
	// A FileSet records where each parsed node sits in the source.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	lines := newLineIndex(src)
	offset := func(p token.Pos) int { return fset.Position(p).Offset }

	var out []store.Chunk
	// region packs one stretch of the file and appends its chunks.
	region := func(s span, heading string) {
		out = append(out, toChunks(src, pack(src, paragraphs(src, s), lim), heading, lines)...)
	}

	prev := 0
	for i, d := range f.Decls {
		start := offset(d.Pos())
		if doc := declDoc(d); doc != nil {
			start = offset(doc.Pos())
		}
		end := offset(d.End())
		if start > prev {
			heading := ""
			if i == 0 {
				heading = "package " + f.Name.Name
			}
			region(span{prev, start}, heading)
		}
		region(span{start, end}, declName(d))
		prev = end
	}
	if prev < len(src) {
		heading := ""
		if len(f.Decls) == 0 {
			heading = "package " + f.Name.Name
		}
		region(span{prev, len(src)}, heading)
	}
	return out, nil
}

// declDoc returns a declaration's doc comment, or nil when it has none. The
// switch below is a type switch: it branches on which concrete type the
// ast.Decl interface value holds.
func declDoc(d ast.Decl) *ast.CommentGroup {
	switch d := d.(type) {
	case *ast.FuncDecl:
		return d.Doc
	case *ast.GenDecl:
		return d.Doc
	}
	return nil
}

// declName names a declaration for a chunk heading: the function name, or
// Receiver.Method for a method, the declared names for type, var and const,
// and "import" for an import block.
func declName(d ast.Decl) string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil && len(d.Recv.List) > 0 {
			if recv := typeName(d.Recv.List[0].Type); recv != "" {
				return recv + "." + d.Name.Name
			}
		}
		return d.Name.Name
	case *ast.GenDecl:
		if d.Tok == token.IMPORT {
			return "import"
		}
		var names []string
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.TypeSpec:
				names = append(names, s.Name.Name)
			case *ast.ValueSpec:
				for _, n := range s.Names {
					names = append(names, n.Name)
				}
			}
		}
		if len(names) > maxHeadingNames {
			names = append(names[:maxHeadingNames], "…")
		}
		return strings.Join(names, ", ")
	}
	return ""
}

// typeName returns the name of a method receiver's type, without the "*"
// of a pointer receiver or the [T] of a generic type.
func typeName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.StarExpr:
		return typeName(e.X)
	case *ast.IndexExpr:
		return typeName(e.X)
	case *ast.IndexListExpr:
		return typeName(e.X)
	}
	return ""
}
