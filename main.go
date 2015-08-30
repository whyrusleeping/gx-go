package main

import (
	"errors"
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
	importer, err := NewImporter()
	if err != nil {
		fmt.Println(err)
		return
	}
	hash, err := importer.GxPublishGoPackage(os.Args[1])
	if err != nil {
		log.Println(err)
		return
	}
	fmt.Println("SUCCESS: ", hash)
}

func pathIsNotStdlib(path string) bool {
	if strings.HasPrefix(path, "github.com") {
		return true
	}
	return false
}

type Importer struct {
	pkgs   map[string]string
	gopath string
	pm     *gx.PM
}

func NewImporter() (*Importer, error) {
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
		log.Println("IMPoRT ERROR: ", err)
		return "", err
	}

	for _, child := range gopkg.Imports {
		if pathIsNotStdlib(child) {
			childhash, err := i.GxPublishGoPackage(child)
			if err != nil {
				log.Printf("[%s] recurse package error: ", child, err)
				return "", err
			}

			chnameParts := strings.Split(child, "/")

			pkg.Dependencies = append(pkg.Dependencies, &gx.Dependency{
				Hash: childhash,
				Name: chnameParts[len(chnameParts)-1],
			})
		}
	}

	err = gx.SavePackageFile(pkg, pkgFilePath)
	if err != nil {
		return "", err
	}

	hash, err = i.pm.PublishPackage(pkgpath, pkg)
	if err != nil {
		log.Println("publish error: ", err)
		return "", err
	}

	i.pkgs[imppath] = hash
	return hash, nil
}

func GoGet(path string) error {
	return exec.Command("go", "get", path).Run()
}
