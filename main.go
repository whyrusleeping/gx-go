package main

import (
	"bufio"
	"errors"
	"fmt"
	"go/build"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	cli "github.com/codegangsta/cli"
	rw "github.com/whyrusleeping/gx-go/rewrite"
	gx "github.com/whyrusleeping/gx/gxutil"
	. "github.com/whyrusleeping/stump"
)

func main() {
	app := cli.NewApp()
	app.Name = "gx-go-tool"
	app.Author = "whyrusleeping"
	app.Usage = "gx extensions for golang"
	app.Version = "0.2.0"

	var UpdateCommand = cli.Command{
		Name:      "update",
		Usage:     "update a packages imports to a new path",
		ArgsUsage: "[old import] [new import]",
		Action: func(c *cli.Context) {
			if len(c.Args()) < 2 {
				fmt.Println("must specify current and new import names")
				return
			}

			oldimp := c.Args()[0]
			newimp := c.Args()[1]

			err := doUpdate(oldimp, newimp)
			if err != nil {
				Fatal(err)
			}
		},
	}

	var ImportCommand = cli.Command{
		Name:  "import",
		Usage: "import a go package and all its depencies into gx",
		Flags: []cli.Flag{
			cli.BoolFlag{
				Name:  "rewrite",
				Usage: "rewrite import paths to use vendored packages",
			},
			cli.BoolFlag{
				Name:  "yesall",
				Usage: "assume defaults for all options",
			},
		},
		Action: func(c *cli.Context) {
			importer, err := NewImporter(c.Bool("rewrite"))
			if err != nil {
				Fatal(err)
			}

			importer.yesall = c.Bool("yesall")

			if !c.Args().Present() {
				Fatal("must specify a package name")
			}

			pkg := c.Args().First()
			Log("vendoring package %s", pkg)

			_, err = importer.GxPublishGoPackage(pkg)
			if err != nil {
				Fatal(err)
			}
		},
	}

	var PathCommand = cli.Command{
		Name:  "path",
		Usage: "prints the import path of the current package within GOPATH",
		Action: func(c *cli.Context) {
			gopath := os.Getenv("GOPATH")
			if gopath == "" {
				Fatal("GOPATH not set, cannot derive import path")
			}

			cwd, err := os.Getwd()
			if err != nil {
				Fatal(err)
			}

			srcdir := path.Join(gopath, "src")
			srcdir += "/"

			if !strings.HasPrefix(cwd, srcdir) {
				Fatal("package not within GOPATH/src")
			}

			rel := cwd[len(srcdir):]
			fmt.Println(rel)
		},
	}

	var HookCommand = cli.Command{
		Name:  "hook",
		Usage: "go specific hooks to be called by the gx tool",
		Action: func(c *cli.Context) {
			if !c.Args().Present() {
				Fatal("no hook specified!")
			}
			sub := c.Args().First()

			pkg, err := gx.LoadPackageFile(gx.PkgFileName)
			if err != nil {
				Fatal(err)
			}

			switch sub {
			case "post-import":
				err := postImportHook(pkg, c.Args().Tail())
				if err != nil {
					Fatal(err)
				}
			}
		},
	}

	app.Commands = []cli.Command{
		UpdateCommand,
		ImportCommand,
		PathCommand,
		HookCommand,
	}

	app.Run(os.Args)
}

func doUpdate(oldimp, newimp string) error {
	curpath, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("error getting working dir: ", err)
	}

	rwf := func(in string) string {
		if in == oldimp {
			return newimp
		}
		return in
	}

	filter := func(in string) bool {
		return strings.HasSuffix(in, ".go")
	}

	return rw.RewriteImports(curpath, rwf, filter)
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
}

func NewImporter(rw bool) (*Importer, error) {
	gp, err := getGoPath()
	if err != nil {
		return nil, err
	}

	cfg, err := gx.LoadConfig()
	if err != nil {
		return nil, err
	}

	return &Importer{
		pkgs:    make(map[string]*gx.Dependency),
		gopath:  gp,
		pm:      gx.NewPM(cfg),
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
		if !i.yesall {
			p := fmt.Sprintf("enter name for import '%s'", imppath)
			nname, err := prompt(p, pkgname)
			if err != nil {
				return nil, err
			}

			pkgname = nname
		}

		err = i.pm.InitPkg(pkgpath, pkgname, "go")
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
		Log("- processing dep %s for %s [%d / %d]", child, imppath, n+1, len(depsToVendor))
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
		fullpkgpath, err := filepath.Abs(pkgpath)
		if err != nil {
			return nil, err
		}

		err = i.rewriteImports(fullpkgpath)
		if err != nil {
			return nil, err
		}
	}

	hash, err := i.pm.PublishPackage(pkgpath, pkg)
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

func (i *Importer) rewriteImports(pkgpath string) error {

	filter := func(p string) bool {
		return !strings.HasPrefix(p, "vendor") &&
			!strings.HasPrefix(p, ".git") &&
			strings.HasSuffix(p, ".go")
	}

	rwf := func(in string) string {
		dep, ok := i.pkgs[in]
		if !ok {
			return in
		}

		return dep.Hash + "/" + dep.Name
	}

	return rw.RewriteImports(pkgpath, rwf, filter)
}

// TODO: take an option to grab packages from local GOPATH
func GoGet(path string) error {
	out, err := exec.Command("go", "get", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("go get failed: %s - %s", string(out), err)
	}
	return nil
}

func prompt(text, def string) (string, error) {
	scan := bufio.NewScanner(os.Stdin)
	fmt.Printf("%s (default: '%s') ", text, def)
	for scan.Scan() {
		if scan.Text() != "" {
			return scan.Text(), nil
		}
		return def, nil
	}

	return "", scan.Err()
}

func yesNoPrompt(prompt string, def bool) bool {
	opts := "[y/N]"
	if def {
		opts = "[Y/n]"
	}

	fmt.Printf("%s %s ", prompt, opts)
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		val := strings.ToLower(scan.Text())
		switch val {
		case "":
			return def
		case "y":
			return true
		case "n":
			return false
		default:
			fmt.Println("please type 'y' or 'n'")
		}
	}

	panic("unexpected termination of stdin")
}

func postImportHook(pkg *gx.Package, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("post-import hook: argument expected")
	}

	npkgHash := args[0]
	npkgPath := filepath.Join("vendor", npkgHash)

	npkg, err := gx.FindPackageInDir(npkgPath)
	if err != nil {
		return err
	}

	if npkg.Go != nil && npkg.Go.DvcsImport != "" {
		q := fmt.Sprintf("update imports of %s to the newly imported package?", npkg.Go.DvcsImport)
		if yesNoPrompt(q, false) {
			nimp := fmt.Sprintf("%s/%s", npkgHash, npkg.Name)
			err := doUpdate(npkg.Go.DvcsImport, nimp)
			if err != nil {
				return err
			}
		}
	}

	return nil
}
