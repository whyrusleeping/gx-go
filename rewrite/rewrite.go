package rewrite

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	fs "github.com/kr/fs"
)

var bufpool *sync.Pool

func init() {
	bufpool = &sync.Pool{
		New: func() interface{} {
			return new(bytes.Buffer)
		},
	}
}

func RewriteImports(path string, rw func(string) string, filter func(string) bool) error {
	w := fs.Walk(path)
	for w.Step() {
		rel := w.Path()[len(path):]
		if len(rel) == 0 {
			continue
		}
		rel = rel[1:]

		if strings.HasPrefix(rel, ".git") || strings.HasPrefix(rel, "vendor") {
			w.SkipDir()
			continue
		}

		if !strings.HasSuffix(w.Path(), ".go") {
			continue
		}

		if !filter(rel) {
			continue
		}

		err := rewriteImportsInFile(w.Path(), rw)
		if err != nil {
			fmt.Println("rewrite error: ", err)
		}
	}
	return nil
}

// inspired by godeps rewrite, rewrites import paths with gx vendored names
func rewriteImportsInFile(fi string, rw func(string) string) error {
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

	buf := bufpool.Get().(*bytes.Buffer)
	if err = cfg.Fprint(buf, fset, file); err != nil {
		return err
	}

	fset = token.NewFileSet()
	file, err = parser.ParseFile(fset, fi, buf, parser.ParseComments)
	if err != nil {
		return err
	}

	buf.Reset()
	bufpool.Put(buf)

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

func fixCanonicalImports(buf []byte) (bool, error) {
	var i int
	var changed bool
	for {
		n, tok, err := bufio.ScanLines(buf[i:], true)
		if err != nil {
			return false, err
		}
		if n == 0 {
			return changed, nil
		}
		i += n

		stripped := stripImportComment(tok)
		if stripped != nil {
			nstr := copy(tok, stripped)
			copy(tok[nstr:], bytes.Repeat([]byte(" "), len(tok)-nstr))
			changed = true
		}
	}
}

// more code from our friends over at godep
const (
	importAnnotation = `import\s+(?:"[^"]*"|` + "`[^`]*`" + `)`
	importComment    = `(?://\s*` + importAnnotation + `\s*$|/\*\s*` + importAnnotation + `\s*\*/)`
)

var (
	importCommentRE = regexp.MustCompile(`\s*(package\s+\w+)\s+` + importComment + `(.*)`)
	pkgPrefix       = []byte("package ")
)

func stripImportComment(line []byte) []byte {
	if !bytes.HasPrefix(line, pkgPrefix) {
		// Fast path; this will skip all but one line in the file.
		// This assumes there is no whitespace before the keyword.
		return nil
	}
	if m := importCommentRE.FindSubmatch(line); m != nil {
		return append(m[1], m[2]...)
	}
	return nil
}
