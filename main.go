package main

import (
	"errors"
	"flag"
	"fmt"
	"go/build"
	"log"
	"os"
	"os/exec"
	"path"
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
	pkgs   map[string]string
	gopath string
	pm     *gx.PM
}

func NewImporter(rw bool) (*Importer, error) {
	gp, err := getGoPath()
	if err != nil {
		return nil, err
	}

	return &Importer{
		pkgs:   make(map[string]string),
		gopath: gp,
		pm:     gx.NewPM(),
	}, nil
}

func getGoPath() (string, error) {
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		return "", errors.New("gopath not set")
	}
	return gopath, nil
}

func (i *Importer) GxPublishGoPackage(imppath string) (hash string, err error) {
	if hash, ok := i.pkgs[imppath]; ok {
		return hash, nil
	}

	// make sure its local
	err = GoGet(imppath)
	if err != nil {
		return "", err
	}

	pkgpath := path.Join(i.gopath, "src", imppath)
	pkgFilePath := path.Join(pkgpath, gx.PkgFileName)
	pkg, err := gx.LoadPackageFile(pkgFilePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}

		// init as gx package
		parts := strings.Split(imppath, "/")
		pkgname := parts[len(parts)-1]
		err = gx.InitPkg(pkgpath, pkgname, "go")
		if err != nil {
			return "", err
		}

		pkg, err = gx.LoadPackageFile(pkgFilePath)
		if err != nil {
			return "", err
		}
	}

	// recurse!
	gopkg, err := build.Import(imppath, "", 0)
	if err != nil {
		return "", err
	}

	var depsToVendor []string

	for _, child := range gopkg.Imports {
		if pathIsNotStdlib(child) {
			depsToVendor = append(depsToVendor, child)
		}
	}

	for n, child := range depsToVendor {
		fmt.Printf("- processing dep %s for %s [%d / %d]\n", child, imppath, n+1, len(depsToVendor))
		childhash, err := i.GxPublishGoPackage(child)
		if err != nil {
			return "", err
		}

		chnameParts := strings.Split(child, "/")

		pkg.Dependencies = append(pkg.Dependencies, &gx.Dependency{
			Hash: childhash,
			Name: chnameParts[len(chnameParts)-1],
		})
	}

	err = gx.SavePackageFile(pkg, pkgFilePath)
	if err != nil {
		return "", err
	}

	hash, err = i.pm.PublishPackage(pkgpath, pkg)
	if err != nil {
		return "", err
	}

	fmt.Printf("published %s as %s\n", imppath, hash)

	i.pkgs[imppath] = hash
	return hash, nil
}

// TODO: take an option to grab packages from local GOPATH
func GoGet(path string) error {
	return exec.Command("go", "get", path).Run()
}
