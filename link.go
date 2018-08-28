package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	cli "github.com/codegangsta/cli"
	gx "github.com/whyrusleeping/gx/gxutil"
)

var pm *gx.PM

var LinkCommand = cli.Command{
	Name:  "link",
	Usage: "Symlink packages to their dvcsimport repos, for local development.",
	Description: `gx-go link eases local development by symlinking actual workspace repositories on demand.

Links the list of dependency packages (specified by name or hash) to the
parent package (in the CWD or specified with --parent). The link operation
is done through the vendor/ directory of the Go project.

If the dependency versions of the parent package don't match the ones in
the dependency being linked use the --sync flag to resolve the conflicts
(the parent's dependency versions will be used instead of the ones in the
linked package). Note that this won't sync dependencies between the
different packages being linked.

Example workflow:

> cd $GOPATH/src/github.com/ipfs/go-ipfs
> gx-go link --sync go-unixfs
Replaced 39 entries in the rewrite map:
  github.com/ipfs/go-ipfs-chunker
  github.com/ipfs/go-ipfs-blockstore
  github.com/libp2p/go-libp2p-net
  [...]
linked go-unixfs /home/user/go/src/github.com/ipfs/go-unixfs

> gx-go link -r -a
unlinked go-unixfs /home/user/go/src/github.com/ipfs/go-unixfs
`,
	Flags: []cli.Flag{
		cli.BoolFlag{
			Name:  "r,remove",
			Usage: "Remove an existing symlink and reinstate the gx package.",
		},
		cli.BoolFlag{
			Name:  "a,all",
			Usage: "Remove all existing symlinks and reinstate the gx packages. Use with -r.",
		},
		cli.StringFlag{
			Name:  "p,parent",
			Usage: "Specify the path of the parent package of the linked dependency (default: CWD).",
		},
		cli.BoolFlag{
			Name:  "s,sync",
			Usage: "Synchronize the dependencies of the linked package with the ones of its parent.",
		},
	},
	Action: func(c *cli.Context) error {
		remove := c.Bool("remove")
		all := c.Bool("all")
		parentPackagePath := c.String("parent")
		sync := c.Bool("sync")

		if parentPackagePath == "" {
			var err error
			parentPackagePath, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("error retrieving the current working directory for the parent package path: %s", err)
			}
		}

		parentPkg, err := LoadPackageFile(filepath.Join(parentPackagePath, gx.PkgFileName))
		if err != nil {
			return fmt.Errorf("parent package not found in %s: %s",
				parentPackagePath, err)
		}

		depRefs := c.Args()[:]
		// It can either be a hash or a name.

		if len(depRefs) == 0 {
			links, err := listLinkedPackages(parentPackagePath)
			if err != nil {
				return err
			}

			if remove && all {
				for _, link := range links {
					depRefs = append(depRefs, link[0])
				}
			}

			if !remove {
				for _, link := range links {
					fmt.Printf("%s %s\n", link[0], link[1])
				}
				return nil
			}
		}

		for _, ref := range depRefs {
			dep := parentPkg.FindDep(ref)
			if dep == nil {
				return fmt.Errorf("dependency reference not found in the parent package: %s", ref)
			}

			if remove {
				target, err := unlinkDependency(dep, parentPackagePath)
				if err != nil {
					return err
				}
				fmt.Printf("unlinked %s %s\n", dep.Name, target)
			} else {
				target, err := linkDependency(dep, parentPackagePath, sync)
				if err != nil {
					return err
				}
				fmt.Printf("linked %s %s\n", dep.Name, target)
			}
		}

		return nil
	},
}

// TODO: Make this function work at the `Package` abstraction level,
// independent of the hashes.
func listLinkedPackages(parentPackagePath string) ([][]string, error) {

	var links [][]string

	gxbase := filepath.Join(parentPackagePath, "vendor", "gx", "ipfs")

	filepath.Walk(gxbase, func(path string, fi os.FileInfo, err error) error {
		relpath, err := filepath.Rel(gxbase, path)
		if err != nil {
			return err
		}

		parts := strings.Split(relpath, string(os.PathSeparator))
		if len(parts) != 2 {
			return nil
		}

		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			links = append(links, []string{parts[0], target})
		}

		return nil
	})

	return links, nil
}

// Link the dependency package `dep` to the parent package located in
// `parentPackagePath` through its `vendor/` directory.
//
// The dependency is first fetched to find its
// DVCS import path (`gx get`), then the repository is fetched through
// `go get` and linked (respecting the path of the Gx global workspace
// to use the same rewrite process of `gx-go rw`):
//   `vendor/gx/ipfs/<pkg-hash>/<pkg-name/` ->  `$GOPATH/src/<dvcs-import>`
//                              (`target`)  ->   (`linkPath`)
//
// If `sync` is set pass the option to the `post-install` hook to sync
// dependency versions.
func linkDependency(dep *gx.Dependency, parentPackagePath string, sync bool) (string, error) {
	gxSrcDir, err := gx.InstallPath("go", "", true)
	if err != nil {
		return "", err
	}

	dvcsImport, err := findDepDVCSimport(dep, gxSrcDir)
	if err != nil {
		return "", fmt.Errorf("error trying to get the DVCS import" +
			"of the dependeny %s: %s", dep.Name, err)
	}

	target := filepath.Join(gxSrcDir, dvcsImport)

	vendorDir := filepath.Join(parentPackagePath, "vendor")
	os.MkdirAll(filepath.Join(vendorDir, "gx", "ipfs", dep.Hash), os.ModePerm)
	linkPath := filepath.Join(vendorDir, "gx", "ipfs", dep.Hash, dep.Name)
	// TODO: Encapsulate paths in a function call (or global setting).

	// Linked package directory, needed for the `post-install` hook.
	linkPackageDir := filepath.Join(vendorDir, "gx", "ipfs", dep.Hash)
	// TODO: this shouldn't be necessary, we should be able to just pass the
	// `linkPath` (i.e., the directory with the name of the package).

	_, err = os.Stat(target)
	if os.IsNotExist(err) {
		goget := exec.Command("go", "get", dvcsImport+"/...")
		goget.Stdout = nil
		goget.Stderr = os.Stderr
		if err = goget.Run(); err != nil {
			return "", fmt.Errorf("error during go get: %s", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("error during os.Stat: %s", err)
	}

	err = os.RemoveAll(linkPath)
	if err != nil {
		return "", fmt.Errorf("error during os.RemoveAll: %s", err)
	}

	err = os.Symlink(target, linkPath)
	if err != nil {
		return "", fmt.Errorf("error during os.Symlink: %s", err)
	}

	gxinst := exec.Command("gx", "install")
	gxinst.Dir = target
	gxinst.Stdout = nil
	gxinst.Stderr = os.Stderr
	if err = gxinst.Run(); err != nil {
		return "", fmt.Errorf("error during gx install: %s", err)
	}

	rwcmdArgs := []string{"hook", "post-install", linkPackageDir}
	if sync {
		rwcmdArgs = append(rwcmdArgs, "--sync", parentPackagePath)
	}
	rwcmd := exec.Command("gx-go", rwcmdArgs...)
	rwcmd.Dir = target
	rwcmd.Stdout = os.Stdout
	rwcmd.Stderr = os.Stderr
	if err := rwcmd.Run(); err != nil {
		return "", fmt.Errorf("error during gx-go rw: %s", err)
	}
	// TODO: Wrap command calls in a function.

	return target, nil
}

// Return the DVCS import path of a dependency (fetching it
// if necessary).
func findDepDVCSimport(dep *gx.Dependency, gxSrcDir string) (string, error) {
	gxdir := filepath.Join(gxSrcDir, "gx", "ipfs", dep.Hash)

	// Get the dependency to find out its DVCS import.
	gxget := exec.Command("gx", "get", dep.Hash, "-o", gxdir)
	gxget.Stdout = os.Stderr
	gxget.Stderr = os.Stderr
	if err := gxget.Run(); err != nil {
		return "", fmt.Errorf("error during gx get: %s", err)
	}

	var pkg gx.Package
	err := gx.FindPackageInDir(&pkg, gxdir)
	if err != nil {
		return "", fmt.Errorf("error during gx.FindPackageInDir: %s", err)
	}

	return GxDvcsImport(&pkg), nil
}

// rm -rf $GOPATH/src/gx/ipfs/$hash
// gx get $hash
func unlinkDependency(dep *gx.Dependency, parentPackagePath string) (string, error) {
	gxSrcDir, err := gx.InstallPath("go", "", true)
	if err != nil {
		return "", err
	}
	dvcsImport, err := findDepDVCSimport(dep, gxSrcDir)
	if err != nil {
		return "", fmt.Errorf("error trying to get the DVCS import of the dependeny %s: %s", dep.Name, err)
	}

	err = os.RemoveAll(filepath.Join(parentPackagePath, "vendor", "gx", "ipfs", dep.Hash))
	if err != nil {
		return "", fmt.Errorf("error during os.RemoveAll: %s", err)
	}

	target := filepath.Join(gxSrcDir, dvcsImport)

	uwcmd := exec.Command("gx-go", "uw")
	uwcmd.Dir = target
	uwcmd.Stdout = nil
	uwcmd.Stderr = os.Stderr
	if err := uwcmd.Run(); err != nil {
		return "", fmt.Errorf("error during gx-go uw: %s", err)
	}

	// If `vendor/gx/ipfs/` is empty remove it.
	if dirEntries(filepath.Join(parentPackagePath, "vendor", "gx", "ipfs")) == 0 &&
		dirEntries(filepath.Join(parentPackagePath, "vendor", "gx")) == 1 &&
			dirEntries(filepath.Join(parentPackagePath, "vendor")) == 1 {

		err = os.RemoveAll(filepath.Join(parentPackagePath, "vendor"))
		if err != nil {
			return "", fmt.Errorf("error during os.RemoveAll: %s", err)
		}
	}
	// TODO: This check could be done in a more elegant way.

	return target, nil
}

// Return the number of entries in the directory (mask errors as 0).
func dirEntries(name string) int {
	entries, err := ioutil.ReadDir(name)
	if err != nil {
		return 0
	}
	return len(entries)
}

func GxDvcsImport(pkg *gx.Package) string {
	pkggx := make(map[string]interface{})
	_ = json.Unmarshal(pkg.Gx, &pkggx)
	return pkggx["dvcsimport"].(string)
}
