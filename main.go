package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/printer"
	"go/token"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	gx "github.com/whyrusleeping/gx/gxutil"
)

func main() {
	rw := flag.Bool("rewrite", false, "rewrite import paths to use vendored packages")
	flag.Parse()

	importer, err := NewImporter(*rw)
	if err != nil {
		fmt.Println(err)
		return
	}

	if len(flag.Args()) == 0 {
		fmt.Println("must specify a package name")
		return
	}

	pkg := flag.Args()[0]
	fmt.Printf("vendoring package %s\n", pkg)

	_, err = importer.GxPublishGoPackage(pkg)
	if err != nil {
		log.Println(err)
		return
	}
}

func pathIsNotStdlib(path string) bool {
	first := strings.Split(path, "/")[0]

	if len(strings.Split(first, ".")) > 1 {
		return true
	}
	return false
}

type Importer struct {
	pkgs    map[string]*gx.Dependency
	gopath  string
	pm      *gx.PM
	rewrite bool
}

func NewImporter(rw bool) (*Importer, error) {
	gp, err := getGoPath()
	if err != nil {
		return nil, err
	}

	return &Importer{
		pkgs:    make(map[string]*gx.Dependency),
		gopath:  gp,
		pm:      gx.NewPM(),
		rewrite: rw,
	}, nil
}

func getGoPath() (string, error) {
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		return "", errors.New("gopath not set")
	}
	return gopath, nil
}

func (i *Importer) GxPublishGoPackage(imppath string) (*gx.Dependency, error) {
	if d, ok := i.pkgs[imppath]; ok {
		return d, nil
	}

	// make sure its local
	err := GoGet(imppath)
	if err != nil {
		return nil, err
	}

	pkgpath := path.Join(i.gopath, "src", imppath)
	pkgFilePath := path.Join(pkgpath, gx.PkgFileName)
	pkg, err := gx.LoadPackageFile(pkgFilePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}

		// init as gx package
		parts := strings.Split(imppath, "/")
		pkgname := parts[len(parts)-1]
		err = gx.InitPkg(pkgpath, pkgname, "go")
		if err != nil {
			return nil, err
		}

		pkg, err = gx.LoadPackageFile(pkgFilePath)
		if err != nil {
			return nil, err
		}
	}

	// recurse!
	gopkg, err := build.Import(imppath, "", 0)
	if err != nil {
		return nil, err
	}

	var depsToVendor []string

	for _, child := range gopkg.Imports {
		if pathIsNotStdlib(child) {
			depsToVendor = append(depsToVendor, child)
		}
	}

	for n, child := range depsToVendor {
		fmt.Printf("- processing dep %s for %s [%d / %d]\n", child, imppath, n+1, len(depsToVendor))
		childdep, err := i.GxPublishGoPackage(child)
		if err != nil {
			return nil, err
		}

		pkg.Dependencies = append(pkg.Dependencies, childdep)
	}

	err = gx.SavePackageFile(pkg, pkgFilePath)
	if err != nil {
		return nil, err
	}

	if i.rewrite {
		err = i.rewriteImports(pkgpath)
		if err != nil {
			return nil, err
		}
	}

	hash, err := i.pm.PublishPackage(pkgpath, pkg)
	if err != nil {
		return nil, err
	}

	fmt.Printf("published %s as %s\n", imppath, hash)

	dep := &gx.Dependency{
		Hash: hash,
		Name: pkg.Name,
	}
	i.pkgs[imppath] = dep
	return dep, nil
}

func (i *Importer) rewriteImports(pkgpath string) error {
	fullpkgpath, err := filepath.Abs(pkgpath)
	if err != nil {
		return err
	}
	return filepath.Walk(pkgpath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		rel := path[len(fullpkgpath)+1:]

		if strings.HasPrefix(rel, "vendor") {
			return nil
		}

		if strings.HasPrefix(rel, ".git") {
			return nil
		}

		if strings.HasSuffix(path, ".go") {
			err := i.rewriteImportsInFile(path)
			if err != nil {
				fmt.Println("ERROR: ", err)
				return err
			}
		}

		return nil
	})
}

// inspired by godeps rewrite, rewrites import paths with gx vendored names
func (i *Importer) rewriteImportsInFile(fi string) error {
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

		dep, ok := i.pkgs[p]
		if !ok {
			continue
		}

		changed = true
		imp.Path.Value = strconv.Quote(dep.Hash + "/" + dep.Name)
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

// TODO: take an option to grab packages from local GOPATH
func GoGet(path string) error {
	return exec.Command("go", "get", path).Run()
}
