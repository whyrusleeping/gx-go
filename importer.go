package main

import (
	"fmt"
	"go/build"
	"go/scanner"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	rw "github.com/whyrusleeping/gx-go/rewrite"
	gx "github.com/whyrusleeping/gx/gxutil"
	. "github.com/whyrusleeping/stump"
)

func doUpdate(dir, oldimp, newimp string) error {
	rwf := func(in string) string {
		if in == oldimp {
			return newimp
		}

		if strings.HasPrefix(in, oldimp+"/") {
			return strings.Replace(in, oldimp, newimp, 1)
		}

		return in
	}

	filter := func(in string) bool {
		return strings.HasSuffix(in, ".go") && !strings.HasPrefix(in, "vendor")
	}

	return rw.RewriteImports(dir, rwf, filter)
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
	yesall  bool
	preMap  map[string]string

	bctx build.Context
}

func NewImporter(rw bool, gopath string, premap map[string]string) (*Importer, error) {
	cfg, err := gx.LoadConfig()
	if err != nil {
		return nil, err
	}

	pm, err := gx.NewPM(cfg)
	if err != nil {
		return nil, err
	}

	if premap == nil {
		premap = make(map[string]string)
	}

	bctx := build.Default
	bctx.GOPATH = gopath

	return &Importer{
		pkgs:    make(map[string]*gx.Dependency),
		gopath:  gopath,
		pm:      pm,
		rewrite: rw,
		preMap:  premap,
		bctx:    bctx,
	}, nil
}

// this function is an attempt to keep subdirectories of a package as part of
// the same logical gx package. It has a special case for golang.org/x/ packages
func getBaseDVCS(path string) string {
	parts := strings.Split(path, "/")
	depth := 3
	/*
		if parts[0] == "golang.org" && parts[1] == "x" {
			depth = 4
		}
	*/

	if len(parts) > depth {
		return strings.Join(parts[:3], "/")
	}
	return path
}

func (i *Importer) GxPublishGoPackage(imppath string) (*gx.Dependency, error) {
	imppath = getBaseDVCS(imppath)
	if d, ok := i.pkgs[imppath]; ok {
		return d, nil
	}

	if hash, ok := i.preMap[imppath]; ok {
		pkg, err := i.pm.GetPackageTo(hash, filepath.Join(vendorDir, hash))
		if err != nil {
			return nil, err
		}

		dep := &gx.Dependency{
			Hash:    hash,
			Name:    pkg.Name,
			Version: pkg.Version,
		}
		i.pkgs[imppath] = dep
		return dep, nil
	}

	// make sure its local
	err := i.GoGet(imppath)
	if err != nil {
		if !strings.Contains(err.Error(), "no buildable Go source files") {
			Error("go get %s failed: %s", imppath, err)
			return nil, err
		}
	}

	pkgpath := path.Join(i.gopath, "src", imppath)
	pkgFilePath := path.Join(pkgpath, gx.PkgFileName)
	pkg, err := LoadPackageFile(pkgFilePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}

		// init as gx package
		parts := strings.Split(imppath, "/")
		pkgname := parts[len(parts)-1]
		if !i.yesall {
			p := fmt.Sprintf("enter name for import '%s'", imppath)
			nname, err := prompt(p, pkgname)
			if err != nil {
				return nil, err
			}

			pkgname = nname
		}

		err = i.pm.InitPkg(pkgpath, pkgname, "go", nil)
		if err != nil {
			return nil, err
		}

		pkg, err = LoadPackageFile(pkgFilePath)
		if err != nil {
			return nil, err
		}
	}

	// wipe out existing dependencies
	pkg.Dependencies = nil

	// recurse!
	depsToVendor, err := i.depsToVendorForPackage(imppath)
	if err != nil {
		return nil, fmt.Errorf("error fetching deps for %s: %s", imppath, err)
	}

	for n, child := range depsToVendor {
		Log("- processing dep %s for %s [%d / %d]", child, imppath, n+1, len(depsToVendor))
		if strings.HasPrefix(child, imppath) {
			continue
		}
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

	fullpkgpath, err := filepath.Abs(pkgpath)
	if err != nil {
		return nil, err
	}

	err = i.rewriteImports(fullpkgpath)
	if err != nil {
		return nil, fmt.Errorf("rewriting imports failed: %s", err)
	}

	err = writeGxIgnore(pkgpath, []string{"Godeps/*"})
	if err != nil {
		return nil, err
	}

	hash, err := i.pm.PublishPackage(pkgpath, &pkg.PackageBase)
	if err != nil {
		return nil, err
	}

	Log("published %s as %s", imppath, hash)

	dep := &gx.Dependency{
		Hash:    hash,
		Name:    pkg.Name,
		Version: pkg.Version,
	}
	i.pkgs[imppath] = dep
	return dep, nil
}

func (i *Importer) depsToVendorForPackage(path string) ([]string, error) {
	rdeps := make(map[string]struct{})

	gopkg, err := i.bctx.Import(path, "", 0)
	if err != nil {
		switch err := err.(type) {
		case *build.NoGoError:
			// if theres no go code here, there still might be some in lower directories
		case scanner.ErrorList:
			Error("failed to scan file: %s", err)
			// continue anyway
		case *build.MultiplePackageError:
			Error("multiple package error: %s", err)
		default:
			Error("ERROR OF TYPE: %#v", err)
			return nil, err
		}

	} else {
		imps := append(gopkg.Imports, gopkg.TestImports...)
		// if the package existed and has go code in it
		gdeps := getBaseDVCS(path) + "/Godeps/_workspace/src/"
		for _, child := range imps {
			if strings.HasPrefix(child, gdeps) {
				child = child[len(gdeps):]
			}

			child = getBaseDVCS(child)
			if pathIsNotStdlib(child) && !strings.HasPrefix(child, path) {
				rdeps[child] = struct{}{}
			}
		}
	}

	dirents, err := ioutil.ReadDir(filepath.Join(i.gopath, "src", path))
	if err != nil {
		return nil, err
	}

	for _, e := range dirents {
		if !e.IsDir() || skipDir(e.Name()) {
			continue
		}

		out, err := i.depsToVendorForPackage(filepath.Join(path, e.Name()))
		if err != nil {
			return nil, err
		}

		for _, o := range out {
			rdeps[o] = struct{}{}
		}
	}

	var depsToVendor []string
	for d, _ := range rdeps {
		depsToVendor = append(depsToVendor, d)
	}

	return depsToVendor, nil
}

func skipDir(name string) bool {
	switch name {
	case "Godeps", "vendor", ".git":
		return true
	default:
		return false
	}
}

func (i *Importer) rewriteImports(pkgpath string) error {

	filter := func(p string) bool {
		return !strings.HasPrefix(p, "vendor") &&
			!strings.HasPrefix(p, ".git") &&
			strings.HasSuffix(p, ".go") &&
			!strings.HasPrefix(p, "Godeps")
	}

	base := pkgpath[len(i.gopath)+5:]
	gdepath := base + "/Godeps/_workspace/src/"
	rwf := func(in string) string {
		if strings.HasPrefix(in, gdepath) {
			in = in[len(gdepath):]
		}

		if !i.rewrite {
			// if rewrite not specified, just fixup godeps paths
			return in
		}

		dep, ok := i.pkgs[in]
		if ok {
			return "gx/" + dep.Hash + "/" + dep.Name
		}

		parts := strings.Split(in, "/")
		if len(parts) > 3 {
			obase := strings.Join(parts[:3], "/")
			dep, bok := i.pkgs[obase]
			if !bok {
				return in
			}

			return strings.Replace(in, obase, "gx/"+dep.Hash+"/"+dep.Name, 1)
		}

		return in
	}

	return rw.RewriteImports(pkgpath, rwf, filter)
}

// TODO: take an option to grab packages from local GOPATH
func (imp *Importer) GoGet(path string) error {
	cmd := exec.Command("go", "get", path)
	env := os.Environ()
	for i, e := range env {
		if strings.HasPrefix(e, "GOPATH=") {
			env[i] = "GOPATH=" + imp.gopath
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go get failed: %s - %s", string(out), err)
	}
	return nil
}

func writeGxIgnore(dir string, ignore []string) error {
	return ioutil.WriteFile(filepath.Join(dir, ".gxignore"), []byte(strings.Join(ignore, "\n")), 0644)
}
