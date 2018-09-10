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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	cli "github.com/codegangsta/cli"
	homedir "github.com/mitchellh/go-homedir"
	rw "github.com/whyrusleeping/gx-go/rewrite"
	gx "github.com/whyrusleeping/gx/gxutil"
	. "github.com/whyrusleeping/stump"
)

var vendorDir = filepath.Join("vendor", "gx", "ipfs")

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
	app.Version = "1.8.0"
	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "verbose",
			Usage: "turn on verbose output",
		},
	}
	app.Before = func(c *cli.Context) error {
		Verbose = c.Bool("verbose")
		return nil
	}

	mcwd, err := os.Getwd()
	if err != nil {
		Fatal("failed to get cwd:", err)
	}
	lcwd, err := filepath.EvalSymlinks(mcwd)
	if err != nil {
		Fatal("failed to resolve symlinks of cdw:", err)
	}
	cwd = lcwd

	app.Commands = []cli.Command{
		DepMapCommand,
		HookCommand,
		ImportCommand,
		PathCommand,
		RewriteCommand,
		rewriteUndoAlias,
		UpdateCommand,
		DvcsDepsCommand,
		LinkCommand,

		DevCopyCommand,
		// Go tool compat:
		GetCommand,
	}

	if err := app.Run(os.Args); err != nil {
		Fatal("Error: " + err.Error())
	}
}

var DepMapCommand = cli.Command{
	Name:  "dep-map",
	Usage: "prints out a json dep map for usage by 'import --map'",
	Action: func(c *cli.Context) error {
		pkg, err := LoadPackageFile(gx.PkgFileName)
		if err != nil {
			return err
		}

		m := make(map[string]string)
		err = buildMap(pkg, m)
		if err != nil {
			return err
		}

		out, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}

		os.Stdout.Write(out)
		return nil
	},
}

var HookCommand = cli.Command{
	Name:  "hook",
	Usage: "go specific hooks to be called by the gx tool",
	Subcommands: []cli.Command{
		postImportCommand,
		reqCheckCommand,
		installLocHookCommand,
		postInitHookCommand,
		postUpdateHookCommand,
		postInstallHookCommand,
		preTestHookCommand,
		postTestHookCommand,
		testHookCommand,
	},
	Action: func(c *cli.Context) error { return nil },
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
		cli.StringFlag{
			Name:  "map",
			Usage: "json document mapping imports to prexisting hashes",
		},
	},
	Action: func(c *cli.Context) error {
		var mapping map[string]string
		preset := c.String("map")
		if preset != "" {
			err := loadMap(&mapping, preset)
			if err != nil {
				return err
			}
		}

		var gopath string
		if c.Bool("tmpdir") {
			dir, err := ioutil.TempDir("", "gx-go-import")
			if err != nil {
				return fmt.Errorf("creating temp dir: %s", err)
			}
			err = os.Setenv("GOPATH", dir)
			if err != nil {
				return fmt.Errorf("setting GOPATH: %s", err)
			}
			Log("setting GOPATH to", dir)

			gopath = dir
		} else {
			gp, err := getGoPath()
			if err != nil {
				return fmt.Errorf("couldnt determine gopath: %s", err)
			}

			gopath = gp
		}

		importer, err := NewImporter(c.Bool("rewrite"), gopath, mapping)
		if err != nil {
			return err
		}

		importer.yesall = c.Bool("yesall")

		if !c.Args().Present() {
			return fmt.Errorf("must specify a package name")
		}

		pkg := c.Args().First()
		Log("vendoring package %s", pkg)

		_, err = importer.GxPublishGoPackage(pkg)
		if err != nil {
			return err
		}

		return nil
	},
}

var UpdateCommand = cli.Command{
	Name:      "update",
	Usage:     "update a packages imports to a new path",
	ArgsUsage: "[old import] [new import]",
	Action: func(c *cli.Context) error {
		if len(c.Args()) < 2 {
			return fmt.Errorf("must specify current and new import names")
		}

		oldimp := c.Args()[0]
		newimp := c.Args()[1]

		err := doUpdate(cwd, oldimp, newimp)
		if err != nil {
			return err
		}

		return nil
	},
}

var rewriteUndoAlias = cli.Command{
	Name: "uw",
	Action: func(c *cli.Context) error {
		return fullRewrite(true)
	},
}

var RewriteCommand = cli.Command{
	Name:      "rewrite",
	Usage:     "temporary hack to evade causality",
	ArgsUsage: "[optional package name]",
	Aliases:   []string{"rw"},
	Flags: []cli.Flag{
		cli.BoolFlag{
			Name:  "undo",
			Usage: "rewrite import paths back to dvcs",
		},
		cli.BoolFlag{
			Name:  "dry-run",
			Usage: "print out mapping without touching files",
		},
		cli.StringFlag{
			Name:  "pkgdir",
			Usage: "alternative location of the package directory",
		},
		cli.BoolFlag{
			Name:  "fix",
			Usage: "more error tolerant version of '--undo'",
		},
	},
	Action: func(c *cli.Context) error {
		root, err := gx.GetPackageRoot()
		if err != nil {
			return err
		}

		if c.Bool("fix") {
			if c.Args().Present() {
				return fmt.Errorf("rewrite: --fix option does not take arguments")
			}
			if c.Bool("dry-run") {
				return fmt.Errorf("rewrite: --fix option does not support --dry-run option")
			}
			return fixImports(root)
		}

		pkg, err := LoadPackageFile(filepath.Join(root, gx.PkgFileName))
		if err != nil {
			return err
		}

		pkgdir := filepath.Join(root, vendorDir)
		if pdopt := c.String("pkgdir"); pdopt != "" {
			pkgdir = pdopt
		}

		VLog("  - building rewrite mapping")
		mapping := make(map[string]string)

		err = buildRewriteMapping(pkg, pkgdir, mapping, c.Bool("undo"))
		if err != nil {
			return fmt.Errorf("build of rewrite mapping failed:\n%s", err)
		}
		if c.Args().Present() {
			keepSet := map[string]struct{}{}
			for _, arg := range c.Args() {
				keepSet[arg] = struct{}{}
			}
			undo := c.Bool("undo")
			for a, b := range mapping {
				gxPath := b
				if undo {
					gxPath = a
				}
				_, name := path.Split(gxPath)
				if _, ok := keepSet[name]; !ok {
					delete(mapping, a)
				}
			}
		}

		VLog("  - rewrite mapping complete")

		if c.Bool("dry-run") {
			tabPrintSortedMap(nil, mapping)
			return nil
		}

		err = doRewrite(pkg, root, mapping)
		if err != nil {
			return err
		}

		return nil
	},
}

var DvcsDepsCommand = cli.Command{
	Name:  "dvcs-deps",
	Usage: "display all dvcs deps",
	Action: func(c *cli.Context) error {
		i, err := NewImporter(false, os.Getenv("GOPATH"), nil)
		if err != nil {
			return err
		}

		relp, err := getImportPath(cwd)
		if err != nil {
			return err
		}

		deps, err := i.DepsToVendorForPackage(relp)
		if err != nil {
			return err
		}

		sort.Strings(deps)
		for _, d := range deps {
			fmt.Println(d)
		}

		return nil
	},
}

func getImportPath(pkgpath string) (string, error) {
	pkg, err := LoadPackageFile(filepath.Join(pkgpath, gx.PkgFileName))
	if err != nil {
		return "", err
	}
	return pkg.Gx.DvcsImport, nil
}

var PathCommand = cli.Command{
	Name:  "path",
	Usage: "prints the import path of the current package within GOPATH",
	Action: func(c *cli.Context) error {
		rel, err := getImportPath(cwd)
		if err != nil {
			return err
		}

		fmt.Println(rel)
		return nil
	},
}

func goGetPackage(path string) error {
	cmd := exec.Command("go", "get", "-d", path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
	return nil
}

func fixImports(path string) error {
	fixmap := make(map[string]string)
	gopath := os.Getenv("GOPATH")
	rwf := func(imp string) string {
		if strings.HasPrefix(imp, "gx/ipfs/") {
			parts := strings.Split(imp, "/")
			canon := strings.Join(parts[:4], "/")
			rest := strings.Join(parts[4:], "/")
			if rest != "" {
				rest = "/" + rest
			}

			if base, ok := fixmap[canon]; ok {
				return base + rest
			}

			var pkg Package
			err := gx.FindPackageInDir(&pkg, filepath.Join(gopath, "src", canon))
			if err != nil {
				fmt.Println(err)
				return imp
			}
			if pkg.Gx.DvcsImport != "" {
				fixmap[imp] = pkg.Gx.DvcsImport
				return pkg.Gx.DvcsImport + rest
			}
			fmt.Printf("Package %s has no dvcs import set!\n", imp)
		}
		return imp
	}

	filter := func(s string) bool {
		return strings.HasSuffix(s, ".go")
	}
	return rw.RewriteImports(path, rwf, filter)
}

var GetCommand = cli.Command{
	Name:  "get",
	Usage: "gx-ified `go get`",
	Action: func(c *cli.Context) error {
		pkgpath := c.Args().First()
		if err := goGetPackage(pkgpath); err != nil {
			return err
		}

		gpath, err := getGoPath()
		if err != nil {
			return err
		}

		pkgdir := filepath.Join(gpath, "src", pkgpath)

		cmd := exec.Command("gx", "install")
		cmd.Dir = pkgdir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return err
		}

		var pkg Package
		if err := gx.LoadPackageFile(&pkg, filepath.Join(pkgdir, "package.json")); err != nil {
			return err
		}

		depsdir := filepath.Join(pkgdir, vendorDir)
		rwmapping := make(map[string]string)
		if err := buildRewriteMapping(&pkg, depsdir, rwmapping, false); err != nil {
			return err
		}

		if err := doRewrite(&pkg, pkgdir, rwmapping); err != nil {
			return err
		}

		return nil
	},
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
	Action: func(c *cli.Context) error {
		if !c.Args().Present() {
			Fatal("no package specified")
		}
		dephash := c.Args().First()

		pkg, err := LoadPackageFile(gx.PkgFileName)
		if err != nil {
			return err
		}

		err = postImportHook(pkg, dephash)
		if err != nil {
			return err
		}

		return nil
	},
}

var reqCheckCommand = cli.Command{
	Name:  "req-check",
	Usage: "hook called to check if requirements of a package are met",
	Action: func(c *cli.Context) error {
		if !c.Args().Present() {
			Fatal("no package specified")
		}
		pkgpath := c.Args().First()

		err := reqCheckHook(pkgpath)
		if err != nil {
			return err
		}

		return nil
	},
}

var postInitHookCommand = cli.Command{
	Name:  "post-init",
	Usage: "hook called to perform go specific package initialization",
	Action: func(c *cli.Context) error {
		var dir string
		if c.Args().Present() {
			dir = c.Args().First()
		} else {
			dir = cwd
		}

		pkgpath := filepath.Join(dir, gx.PkgFileName)
		pkg, err := LoadPackageFile(pkgpath)
		if err != nil {
			return err
		}

		imp, _ := packagesGoImport(dir)

		if imp != "" {
			pkg.Gx.DvcsImport = imp
		}

		err = gx.SavePackageFile(pkg, pkgpath)
		if err != nil {
			return err
		}

		return nil
	},
}

var postInstallHookCommand = cli.Command{
	Name:  "post-install",
	Usage: "post install hook for newly installed go packages",
	Flags: []cli.Flag{
		cli.BoolFlag{
			Name:  "global",
			Usage: "specifies whether or not the install was global",
		},
	},
	Action: func(c *cli.Context) error {
		if !c.Args().Present() {
			return fmt.Errorf("must specify path to newly installed package")
		}
		npkg := c.Args().First()
		// update sub-package refs here
		// ex:
		// if this package is 'github.com/X/Y' replace all imports
		// matching 'github.com/X/Y*' with 'gx/<hash>/name*'

		var pkg Package
		err := gx.FindPackageInDir(&pkg, npkg)
		if err != nil {
			return fmt.Errorf("find package failed: %s", err)
		}

		dir := filepath.Join(npkg, pkg.Name)

		// build rewrite mapping from parent package if
		// this call is made on one in the vendor directory
		var reldir string
		if strings.Contains(npkg, "vendor/gx/ipfs") {
			reldir = strings.Split(npkg, "vendor/gx/ipfs")[0]
			reldir = filepath.Join(reldir, "vendor", "gx", "ipfs")
		} else {
			reldir = dir
		}

		mapping := make(map[string]string)
		err = buildRewriteMapping(&pkg, reldir, mapping, false)
		if err != nil {
			return fmt.Errorf("building rewrite mapping failed: %s", err)
		}

		hash := filepath.Base(npkg)
		newimp := "gx/ipfs/" + hash + "/" + pkg.Name
		mapping[pkg.Gx.DvcsImport] = newimp

		err = doRewrite(&pkg, dir, mapping)
		if err != nil {
			return fmt.Errorf("rewrite failed: %s", err)
		}

		return nil
	},
}

func doRewrite(pkg *Package, cwd string, mapping map[string]string) error {
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

	VLog("  - rewriting imports")
	err := rw.RewriteImports(cwd, rwm, filter)
	if err != nil {
		return err
	}
	VLog("  - finished!")

	return nil
}

var installLocHookCommand = cli.Command{
	Name:  "install-path",
	Usage: "prints out install path",
	Flags: []cli.Flag{
		cli.BoolFlag{
			Name:  "global",
			Usage: "print global install directory",
		},
	},
	Action: func(c *cli.Context) error {
		if c.Bool("global") {
			gpath, err := getGoPath()
			if err != nil {
				return fmt.Errorf("GOPATH not set")
			}
			fmt.Println(filepath.Join(gpath, "src"))
			return nil
		} else {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("install-path cwd: %s", err)
			}

			fmt.Println(filepath.Join(cwd, "vendor"))
			return nil
		}
	},
}

var postUpdateHookCommand = cli.Command{
	Name:  "post-update",
	Usage: "rewrite go package imports to new versions",
	Action: func(c *cli.Context) error {
		if len(c.Args()) < 2 {
			Fatal("must specify two arguments")
		}
		before := "gx/ipfs/" + c.Args()[0]
		after := "gx/ipfs/" + c.Args()[1]
		err := doUpdate(cwd, before, after)
		if err != nil {
			return err
		}

		return nil
	},
}

var testHookCommand = cli.Command{
	Name:            "test",
	SkipFlagParsing: true,
	Action: func(c *cli.Context) error {
		args := []string{"test"}
		args = append(args, c.Args()...)
		cmd := exec.Command("go", args...)
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		return cmd.Run()
	},
}

var preTestHookCommand = cli.Command{
	Name:  "pre-test",
	Usage: "",
	Action: func(c *cli.Context) error {
		return fullRewrite(false)
	},
}

var postTestHookCommand = cli.Command{
	Name:  "post-test",
	Usage: "",
	Action: func(c *cli.Context) error {
		return fullRewrite(true)
	},
}

var DevCopyCommand = cli.Command{
	Name:  "devcopy",
	Usage: "Create a development copy of the given package",
	Action: func(c *cli.Context) error {
		// gx install --local
		// gx-go rewrite --undo
		// symlink <hash> -> dvcs path

		Log("creating local copy of deps")
		cmd := exec.Command("gx", "install", "--local")
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
		if err := cmd.Run(); err != nil {
			return err
		}

		Log("change imports to dvcs")
		cmd = exec.Command("gx-go", "rewrite", "--undo")
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stdout
		if err := cmd.Run(); err != nil {
			return err
		}

		pkg, err := LoadPackageFile(gx.PkgFileName)
		if err != nil {
			return err
		}

		return devCopySymlinking(filepath.Join(cwd, "vendor"), pkg, make(map[string]bool))
	},
}

func devCopySymlinking(root string, pkg *Package, done map[string]bool) error {
	for _, dep := range pkg.Dependencies {
		if done[dep.Hash] {
			continue
		}
		done[dep.Hash] = true

		var cpkg Package
		err := gx.LoadPackage(&cpkg, pkg.Language, dep.Hash)
		if err != nil {
			if os.IsNotExist(err) {
				VLog("LoadPackage error: ", err)
				return fmt.Errorf("package %s (%s) not found", dep.Name, dep.Hash)
			}
			return err
		}

		frompath := filepath.Join(root, "gx", "ipfs", dep.Hash, dep.Name)
		cmd := exec.Command("gx-go", "rewrite", "--undo")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Dir = frompath
		if err := cmd.Run(); err != nil {
			return err
		}

		topath := filepath.Join(root, cpkg.Gx.DvcsImport)
		dir := filepath.Dir(topath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}

		if err := os.Symlink(frompath, topath); err != nil {
			return err
		}

		if err := devCopySymlinking(root, &cpkg, done); err != nil {
			return err
		}
	}
	return nil
}

func fullRewrite(undo bool) error {
	root, err := gx.GetPackageRoot()
	if err != nil {
		return err
	}

	pkg, err := LoadPackageFile(filepath.Join(root, gx.PkgFileName))
	if err != nil {
		return err
	}

	pkgdir := filepath.Join(root, vendorDir)

	mapping := make(map[string]string)
	err = buildRewriteMapping(pkg, pkgdir, mapping, undo)
	if err != nil {
		return fmt.Errorf("build of rewrite mapping failed:\n%s", err)
	}

	return doRewrite(pkg, root, mapping)
}

func packagesGoImport(p string) (string, error) {
	gopath, err := getGoPath()
	if err != nil {
		return "", err
	}

	srcdir := path.Join(gopath, "src")
	srcdir += "/"

	if !strings.HasPrefix(p, srcdir) {
		return "", fmt.Errorf("package not within GOPATH/src")
	}

	return p[len(srcdir):], nil
}

func postImportHook(pkg *Package, npkgHash string) error {
	var npkg Package
	err := gx.LoadPackage(&npkg, "go", npkgHash)
	if err != nil {
		return err
	}

	if npkg.Gx.DvcsImport != "" {
		q := fmt.Sprintf("update imports of %s to the newly imported package?", npkg.Gx.DvcsImport)
		if yesNoPrompt(q, false) {
			nimp := fmt.Sprintf("gx/ipfs/%s/%s", npkgHash, npkg.Name)
			err := doUpdate(cwd, npkg.Gx.DvcsImport, nimp)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func reqCheckHook(pkgpath string) error {
	var npkg Package
	pkgfile := filepath.Join(pkgpath, gx.PkgFileName)
	err := gx.LoadPackageFile(&npkg, pkgfile)
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
		if parts[2] == "devel" {
			Log("warning: using unknown development version of go, proceed with caution")
			return nil
		}

		havevers := parts[2][2:]

		reqvers := npkg.Gx.GoVersion

		badreq, err := versionComp(havevers, reqvers)
		if err != nil {
			return err
		}
		if badreq {
			return fmt.Errorf("package '%s' requires at least go version %s, you have %s installed.", npkg.Name, reqvers, havevers)
		}

		gxgocompvers := runtime.Version()
		if strings.HasPrefix(gxgocompvers, "devel") {
			return nil
		}
		if strings.HasPrefix(gxgocompvers, "go") {
			badreq, err := versionComp(gxgocompvers[2:], reqvers)
			if err != nil {
				return err
			}
			if badreq {
				return fmt.Errorf("package '%s' requires at least go version %s.\nhowever, your gx-go binary was compiled with %s.\nPlease update gx-go (or recompile with your current go compiler)", npkg.Name, reqvers, gxgocompvers)
			}
		} else {
			Log("gx-go was compiled with an unrecognized version of go. (%s)", gxgocompvers)
			Log("If you encounter any strange issues during its usage, try rebuilding gx-go with go %s or higher", reqvers)
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

	// treat X.YrcZ or X.YbetaZ as simply X.Y
	for _, s := range []string{"rc", "beta"} {
		if strings.Contains(hp[len(hp)-1], s) {
			hp[len(hp)-1] = strings.Split(hp[len(hp)-1], s)[0]
			break
		}
	}

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
		} else if hv > rv {
			return false, nil
		}
	}
	return false, nil
}

func globalPath() string {
	gp, _ := getGoPath()
	return filepath.Join(gp, "src", "gx", "ipfs")
}

func loadDep(dep *gx.Dependency, pkgdir string) (*Package, error) {
	var cpkg Package
	pdir := filepath.Join(pkgdir, dep.Hash)
	VLog("  - fetching dep: %s (%s)", dep.Name, dep.Hash)
	err := gx.FindPackageInDir(&cpkg, pdir)
	if err != nil {
		// try global
		p := filepath.Join(globalPath(), dep.Hash)
		VLog("  - checking in global namespace (%s)", p)
		gerr := gx.FindPackageInDir(&cpkg, p)
		if gerr != nil {
			return nil, fmt.Errorf("failed to find package: %s", gerr)
		}
	}

	return &cpkg, nil
}

// Rewrites the package `DvcsImport` with the dependency hash (or
// the other way around if `undo` is true). `overwrite` indicates
// whether or not to allow overwriting an existing entry in the map.
func addRewriteForDep(dep *gx.Dependency, pkg *Package, m map[string]string, undo bool, overwrite bool) {
	if pkg.Gx.DvcsImport == "" {
		return
		// Nothing to do as there is no DVCS import path.
		// TODO: Should this case be flagged?
	}

	from := pkg.Gx.DvcsImport
	to := "gx/ipfs/" + dep.Hash + "/" + pkg.Name
	if undo {
		from, to = to, from
	}

	_, entryExists := m[from]
	if !entryExists || overwrite {
		m[from] = to
	} else if entryExists && m[from] != to {
		VLog("trying to overwrite rewrite map entry %s pointing to %s with %s", from, m[from], to)
	}
}

func buildRewriteMapping(pkg *Package, pkgdir string, m map[string]string, undo bool) error {
	seen := make(map[string]struct{})
	var process func(pkg *Package, rootPackage bool) error

	// `rootPackage` indicates if we're processing the dependencies
	// of the root package (declared in `package.json`) that should
	// not be overwritten in the map with transitive dependencies
	// (dependencies of other dependencies).
	process = func(pkg *Package, rootPackage bool) error {
		for _, dep := range pkg.Dependencies {
			if _, ok := seen[dep.Hash]; ok {
				continue
			}
			seen[dep.Hash] = struct{}{}

			cpkg, err := loadDep(dep, pkgdir)
			if err != nil {
				VLog("error loading dep %q of %q: %s", dep.Name, pkg.Name, err)
				return fmt.Errorf("package %q not found. (dependency of %s)", dep.Name, pkg.Name)
			}

			// Allow overwriting the map only if these are the dependencies
			// of the root package.
			addRewriteForDep(dep, cpkg, m, undo, rootPackage)

			// recurse!
			err = process(cpkg, false)
			if err != nil {
				return err
			}
		}
		return nil
	}
	return process(pkg, true)
}

func buildMap(pkg *Package, m map[string]string) error {
	for _, dep := range pkg.Dependencies {
		var ch Package
		err := gx.FindPackageInDir(&ch, filepath.Join(vendorDir, dep.Hash))
		if err != nil {
			return err
		}

		if ch.Gx.DvcsImport != "" {
			e, ok := m[ch.Gx.DvcsImport]
			if ok {
				if e != dep.Hash {
					Log("have two dep packages with same import path: ", ch.Gx.DvcsImport)
					Log("  - ", e)
					Log("  - ", dep.Hash)
				}
				continue
			}
			m[ch.Gx.DvcsImport] = dep.Hash
		}

		err = buildMap(&ch, m)
		if err != nil {
			return err
		}
	}
	return nil
}

func loadMap(i interface{}, file string) error {
	fi, err := os.Open(file)
	if err != nil {
		return err
	}
	defer fi.Close()

	return json.NewDecoder(fi).Decode(i)
}

func tabPrintSortedMap(headers []string, m map[string]string) {
	var names []string
	for k, _ := range m {
		names = append(names, k)
	}

	sort.Strings(names)

	w := tabwriter.NewWriter(os.Stdout, 12, 4, 1, ' ', 0)
	if headers != nil {
		fmt.Fprintf(w, "%s\t%s\n", headers[0], headers[1])
	}

	for _, n := range names {
		fmt.Fprintf(w, "%s\t%s\n", n, m[n])
	}
	w.Flush()
}

func getGoPath() (string, error) {
	gp := os.Getenv("GOPATH")
	if gp == "" {
		return homedir.Expand("~/go")
	}

	return filepath.SplitList(gp)[0], nil
}
