package rewrite

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"

	fs "github.com/kr/fs"
)

var bufpool = &sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

var cfg = &printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}

func RewriteImports(ipath string, rw func(string) string, filter func(string) bool) error {
	path, err := filepath.EvalSymlinks(ipath)
	if err != nil {
		return err
	}

	var rwLock sync.Mutex

	var wg sync.WaitGroup
	torewrite := make(chan string)
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range torewrite {
				err := rewriteImportsInFile(path, rw, &rwLock)
				if err != nil {
					fmt.Println("rewrite error: ", err)
				}
			}
		}()
	}

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
		torewrite <- w.Path()
	}
	close(torewrite)
	wg.Wait()
	return nil
}

// inspired by godeps rewrite, rewrites import paths with gx vendored names
func rewriteImportsInFile(fi string, rw func(string) string, rwLock *sync.Mutex) error {
	// 1. Rewrite the imports (if we have any)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, fi, nil, parser.ParseComments|parser.ImportsOnly)
	if err != nil {
		return err
	}
	if len(file.Imports) == 0 {
		return nil
	}

	oldImportsEnd := fset.Position(file.Imports[len(file.Imports)-1].End()).Offset

	rwLock.Lock()
	var changed bool
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			rwLock.Unlock()
			return err
		}

		np := rw(p)

		if np != p {
			changed = true
			imp.Path.Value = strconv.Quote(np)
		}
	}
	rwLock.Unlock()

	if !changed {
		return nil
	}

	buf := bufpool.Get().(*bytes.Buffer)
	defer func() {
		bufpool.Put(buf)
	}()

	// Write them back to a temporary buffer

	buf.Reset()
	if err = cfg.Fprint(buf, fset, file); err != nil {
		return err
	}

	// 2. Read the imports back in to sort them.

	fset = token.NewFileSet()
	file, err = parser.ParseFile(fset, fi, buf, parser.ParseComments|parser.ImportsOnly)
	if err != nil {
		return err
	}

	ast.SortImports(fset, file)

	// Write them back to a temporary buffer

	buf.Reset()
	if err = cfg.Fprint(buf, fset, file); err != nil {
		return err
	}

	// 3. Read them back in to find the new end of the imports.

	fset = token.NewFileSet()
	file, err = parser.ParseFile(fset, fi, buf, parser.ParseComments|parser.ImportsOnly)
	if err != nil {
		return err
	}

	newImportsEnd := fset.Position(file.Imports[len(file.Imports)-1].End()).Offset

	// Write them back to the buffer and truncate.
	buf.Reset()
	if err = cfg.Fprint(buf, fset, file); err != nil {
		return err
	}
	buf.Truncate(newImportsEnd)

	// Finally, build the file.

	tmppath := fi + ".temp"
	tmp, err := os.Create(tmppath)
	if err != nil {
		return err
	}

	src, err := os.Open(fi)
	if err != nil {
		return err
	}
	defer src.Close()

	_, err = src.Seek(int64(oldImportsEnd), io.SeekStart)
	if err != nil {
		return err
	}

	buf.WriteTo(tmp)

	_, err = io.Copy(tmp, src)
	if err != nil {
		return err
	}

	if err = tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmppath, fi)
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
