package rewrite

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"strconv"

	fs "github.com/kr/fs"
)

func RewriteImports(path string, rw func(string) string, filter func(string) bool) error {
	w := fs.Walk(path)
	for w.Step() {
		rel := w.Path()[len(path):]
		if len(rel) == 0 {
			continue
		}
		rel = rel[1:]

		if filter(rel) {
			w.SkipDir()
			continue
		}

		err := rewriteImportsInFile(w.Path(), rw)
		if err != nil {
			fmt.Println("rewrite error: ", err)
			return err
		}
	}
	return nil
}

// inspired by godeps rewrite, rewrites import paths with gx vendored names
func rewriteImportsInFile(fi string, rw func(string) string) error {
	fmt.Println("REWRITE FI: ", fi)
	cfg := &printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, fi, nil, parser.ParseComments)
	if err != nil {
		return err
	}

	var changed bool
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return err
		}

		np := rw(p)

		if np != p {
			changed = true
			imp.Path.Value = strconv.Quote(np)
		}
	}

	if !changed {
		return nil
	}

	var buffer bytes.Buffer
	if err = cfg.Fprint(&buffer, fset, file); err != nil {
		return err
	}
	fset = token.NewFileSet()
	file, err = parser.ParseFile(fset, fi, &buffer, parser.ParseComments)
	ast.SortImports(fset, file)
	wpath := fi + ".temp"
	w, err := os.Create(wpath)
	if err != nil {
		return err
	}
	if err = cfg.Fprint(w, fset, file); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}

	return os.Rename(wpath, fi)
}
