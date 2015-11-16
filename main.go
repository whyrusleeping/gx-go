package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	cli "github.com/codegangsta/cli"
	rw "github.com/whyrusleeping/gx-go/rewrite"
	gx "github.com/whyrusleeping/gx/gxutil"
	. "github.com/whyrusleeping/stump"
)

var cwd string

// for go packages, extra info
type GoInfo struct {
	DvcsImport string `json:"dvcsimport,omitempty"`

	// GoVersion sets a compiler version requirement, users will be warned if installing
	// a package using an unsupported compiler
	GoVersion string `json:"goversion,omitempty"`
}

type Package struct {
	gx.PackageBase

	Gx GoInfo `json:"gx,omitempty"`
}

func LoadPackageFile(name string) (*Package, error) {
	fi, err := os.Open(name)
	if err != nil {
		return nil, err
	}

	var pkg Package
	err = json.NewDecoder(fi).Decode(&pkg)
	if err != nil {
		return nil, err
	}

	return &pkg, nil
}

func main() {
	app := cli.NewApp()
	app.Name = "gx-go"
	app.Author = "whyrusleeping"
	app.Usage = "gx extensions for golang"
	app.Version = "0.2.0"

	mcwd, err := os.Getwd()
	if err != nil {
		Fatal(err)
	}
	cwd = mcwd

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
		Description: `imports a given go package and all of its dependencies into gx
producing a package.json for each, and outputting a package hash
for each.`,
		Flags: []cli.Flag{
			cli.BoolFlag{
				Name:  "rewrite",
				Usage: "rewrite import paths to use vendored packages",
			},
			cli.BoolFlag{
				Name:  "yesall",
				Usage: "assume defaults for all options",
			},
			cli.BoolFlag{
				Name:  "tmpdir",
				Usage: "create and use a temporary directory for the GOPATH",
			},
		},
		Action: func(c *cli.Context) {
			importer, err := NewImporter(c.Bool("rewrite"))
			if err != nil {
				Fatal(err)
			}

			if c.Bool("tmpdir") {
				dir, err := ioutil.TempDir("", "gx-go-import")
				if err != nil {
					Fatal("creating temp dir:", err)
				}
				importer.gopath = dir
				err = os.Setenv("GOPATH", dir)
				if err != nil {
					Fatal("setting GOPATH: ", err)
				}
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

			srcdir := path.Join(gopath, "src")
			srcdir += "/"

			if !strings.HasPrefix(cwd, srcdir) {
				Fatal("package not within GOPATH/src")
			}

			rel := cwd[len(srcdir):]
			fmt.Println(rel)
		},
	}

	var RewriteCommand = cli.Command{
		Name:  "rewrite",
		Usage: "temporary hack to evade causality",
		Flags: []cli.Flag{
			cli.BoolFlag{
				Name:  "undo",
				Usage: "rewrite import paths back to dvcs",
			},
		},
		Action: func(c *cli.Context) {
			pkg, err := LoadPackageFile(gx.PkgFileName)
			if err != nil {
				Fatal(err)
			}

			undo := c.Bool("undo")

			mapping := make(map[string]string)
			err = buildRewriteMapping(pkg, mapping, undo)
			if err != nil {
				Fatal(err)
			}

			rwm := func(in string) string {
				m, ok := mapping[in]
				if ok {
					return m
				}

				for k, v := range mapping {
					if strings.HasPrefix(in, k+"/") {
						nmapping := strings.Replace(in, k, v, 1)
						mapping[in] = nmapping
						return nmapping
					}
				}

				mapping[in] = in
				return in
			}

			filter := func(s string) bool {
				return strings.HasSuffix(s, ".go")
			}

			err = rw.RewriteImports(cwd, rwm, filter)
			if err != nil {
				Fatal(err)
			}
		},
	}

	var HookCommand = cli.Command{
		Name:  "hook",
		Usage: "go specific hooks to be called by the gx tool",
		Subcommands: []cli.Command{
			postImportCommand,
			reqCheckCommand,
			postInitHookCommand,
			postUpdateHookCommand,
		},
		Action: func(c *cli.Context) {},
	}

	app.Commands = []cli.Command{
		UpdateCommand,
		ImportCommand,
		PathCommand,
		HookCommand,
		RewriteCommand,
	}

	app.Run(os.Args)
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

var postImportCommand = cli.Command{
	Name:  "post-import",
	Usage: "hook called after importing a new go package",
	Action: func(c *cli.Context) {
		if !c.Args().Present() {
			Fatal("no package specified")
		}
		dephash := c.Args().First()

		pkg, err := LoadPackageFile(gx.PkgFileName)
		if err != nil {
			Fatal(err)
		}

		err = postImportHook(pkg, dephash)
		if err != nil {
			Fatal(err)
		}
	},
}

var reqCheckCommand = cli.Command{
	Name:  "req-check",
	Usage: "hook called to check if requirements of a package are met",
	Action: func(c *cli.Context) {
		if !c.Args().Present() {
			Fatal("no package specified")
		}
		dephash := c.Args().First()

		err := reqCheckHook(dephash)
		if err != nil {
			Fatal(err)
		}
	},
}

var postInitHookCommand = cli.Command{
	Name:  "post-init",
	Usage: "hook called to perform go specific package initialization",
	Action: func(c *cli.Context) {
		var dir string
		if c.Args().Present() {
			dir = c.Args().First()
		} else {
			dir = cwd
		}

		pkgpath := filepath.Join(dir, gx.PkgFileName)
		pkg, err := LoadPackageFile(pkgpath)
		if err != nil {
			Fatal(err)
		}

		imp, _ := packagesGoImport(dir)

		if imp != "" {
			pkg.Gx.DvcsImport = imp
		}

		err = gx.SavePackageFile(pkg, pkgpath)
		if err != nil {
			Fatal(err)
		}
	},
}

var postUpdateHookCommand = cli.Command{
	Name:  "post-update",
	Usage: "rewrite go package imports to new versions",
	Action: func(c *cli.Context) {
		if len(c.Args()) < 2 {
			Fatal("must specify two arguments")
		}
		before := c.Args()[0]
		after := c.Args()[1]
		err := doUpdate(before, after)
		if err != nil {
			Fatal(err)
		}
	},
}

func packagesGoImport(p string) (string, error) {
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		return "", fmt.Errorf("GOPATH not set, cannot derive import path")
	}

	srcdir := path.Join(gopath, "src")
	srcdir += "/"

	if !strings.HasPrefix(p, srcdir) {
		return "", fmt.Errorf("package not within GOPATH/src")
	}

	return p[len(srcdir):], nil
}

func postImportHook(pkg *Package, npkgHash string) error {
	npkgPath := filepath.Join("vendor", npkgHash)

	var npkg Package
	err := gx.FindPackageInDir(&npkg, npkgPath)
	if err != nil {
		return err
	}

	if npkg.Gx.DvcsImport != "" {
		q := fmt.Sprintf("update imports of %s to the newly imported package?", npkg.Gx.DvcsImport)
		if yesNoPrompt(q, false) {
			nimp := fmt.Sprintf("%s/%s", npkgHash, npkg.Name)
			err := doUpdate(npkg.Gx.DvcsImport, nimp)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func reqCheckHook(pkghash string) error {
	p := filepath.Join("vendor", pkghash)

	var npkg Package
	err := gx.FindPackageInDir(&npkg, p)
	if err != nil {
		return err
	}

	if npkg.Gx.GoVersion != "" {
		out, err := exec.Command("go", "version").CombinedOutput()
		if err != nil {
			return fmt.Errorf("no go compiler installed")
		}

		parts := strings.Split(string(out), " ")
		if len(parts) < 4 {
			return fmt.Errorf("unrecognized output from go compiler")
		}

		havevers := parts[2][2:]

		reqvers := npkg.Gx.GoVersion

		badreq, err := versionComp(havevers, reqvers)
		if err != nil {
			return err
		}
		if badreq {
			return fmt.Errorf("package '%s' requires go version %s, you have %s installed.", npkg.Name, reqvers, havevers)
		}

	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func versionComp(have, req string) (bool, error) {
	hp := strings.Split(have, ".")
	rp := strings.Split(req, ".")

	l := min(len(hp), len(rp))
	hp = hp[:l]
	rp = rp[:l]
	for i, v := range hp {
		hv, err := strconv.Atoi(v)
		if err != nil {
			return false, err
		}

		rv, err := strconv.Atoi(rp[i])
		if err != nil {
			return false, err
		}

		if hv < rv {
			return true, nil
		}
	}
	return false, nil
}

func buildRewriteMapping(pkg *Package, m map[string]string, undo bool) error {
	dir := filepath.Join(cwd, "vendor")
	for _, dep := range pkg.Dependencies {
		var cpkg Package
		pdir := filepath.Join(dir, dep.Hash)
		err := gx.FindPackageInDir(&cpkg, pdir)
		if err != nil {
			Fatal(err)
		}

		if cpkg.Gx.DvcsImport != "" {
			from := cpkg.Gx.DvcsImport
			to := dep.Hash + "/" + cpkg.Name
			if undo {
				from, to = to, from
			}
			m[from] = to
		}

		err = buildRewriteMapping(&cpkg, m, undo)
		if err != nil {
			return err
		}
	}

	return nil
}
