package main

import (
	"encoding/json"
	"fmt"
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

gx-go link replaces the target gx package (either by name or hash) with
a symlink to the appropriate dvcs repo in your GOPATH. To make this
"symlinked" repo usable as a gx package, gx-go link rewrites the target
package's dvcs imports using the target package's package.json.
Unfortunately, this can cause build errors in packages depending on this
package if these dependent packages specify alternative, conflicting
dependency versions. We can work around this using the --override-deps flag 
to rewrite the target package using dependencies from the current package
(the package you're trying to build) first, falling back on the target
package's package.json file.

Example workflow:

> cd $GOPATH/src/github.com/ipfs/go-ipfs
> gx-go link --override-deps go-unixfs
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
		cli.BoolFlag{
			Name:  "o,override-deps",
			Usage: "Override dependency versions of the target package with its current dependant package.",
		},
	},
	Action: func(c *cli.Context) error {
		remove := c.Bool("remove")
		all := c.Bool("all")
		overrideDeps := c.Bool("override-deps")

		depRefs := c.Args()[:]
		// It can either be a hash or a name.

		if len(depRefs) == 0 {
			links, err := listLinkedPackages()
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

		// Path of the current dependant package.
		parentPackagePath, err := gx.GetPackageRoot()
		if err != nil {
			return fmt.Errorf("error retrieving the parent package: %s", err)
		}

		parentPkg, err := LoadPackageFile(filepath.Join(parentPackagePath, gx.PkgFileName))
		if err != nil {
			return fmt.Errorf("parent package not found in %s: %s",
				parentPackagePath, err)
		}

		for _, ref := range depRefs {
			dep := parentPkg.FindDep(ref)
			if dep == nil {
				return fmt.Errorf("dependency reference not found in the parent package: %s", ref)
			}

			if remove {
				target, err := unlinkDependency(dep)
				if err != nil {
					return err
				}
				fmt.Printf("unlinked %s %s\n", dep.Name, target)
			} else {
				target, err := linkDependency(dep, overrideDeps, parentPackagePath)
				if err != nil {
					return err
				}
				fmt.Printf("linked %s %s\n", dep.Name, target)
			}
		}

		return nil
	},
}

func listLinkedPackages() ([][]string, error) {
	var links [][]string

	srcdir, err := gx.InstallPath("go", "", true)
	if err != nil {
		return links, err
	}
	gxbase := filepath.Join(srcdir, "gx", "ipfs")

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

// Link the dependency package `dep` to the global Gx workspace.
//
// The dependency is first fetched to find its DVCS import path (`gx get`),
// then the repository is fetched through `go get` and sym-linked:
//   `$GOPATH/gx/ipfs/<pkg-hash>/<pkg-name>/` ->  `$GOPATH/src/<dvcs-import>`
//                               (`target`)  ->   (`linkPath`)
// If `overrideDeps` is set pass the option to the `post-install` hook to override
// dependency versions.
func linkDependency(dep *gx.Dependency, overrideDeps bool, parentPackagePath string) (string, error) {
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

	// Linked package directory, needed for the `post-install` hook.
	linkPackageDir := filepath.Join(gxSrcDir, "gx", "ipfs", dep.Hash)
	// TODO: this shouldn't be necessary, we should be able to just pass the
	// `linkPath` (i.e., the directory with the name of the package).

	linkPath := filepath.Join(linkPackageDir, dep.Name)

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
	if overrideDeps {
		rwcmdArgs = append(rwcmdArgs, "--override-deps", parentPackagePath)
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
	err := gxGetPackage(dep.Hash)
	if err != nil {
		return "", err
	}

	var pkg gx.Package
	err = gx.FindPackageInDir(&pkg, gxdir)
	if err != nil {
		return "", fmt.Errorf("error during gx.FindPackageInDir: %s", err)
	}

	return GxDvcsImport(&pkg), nil
}

// rm -rf $GOPATH/src/gx/ipfs/$hash
// gx get $hash
func unlinkDependency(dep *gx.Dependency) (string, error) {
	gxSrcDir, err := gx.InstallPath("go", "", true)
	if err != nil {
		return "", err
	}

	dvcsImport, err := findDepDVCSimport(dep, gxSrcDir)
	if err != nil {
		return "", fmt.Errorf("error trying to get the DVCS import of the dependeny %s: %s", dep.Name, err)
	}

	target := filepath.Join(gxSrcDir, dvcsImport)

	uwcmd := exec.Command("gx-go", "rw", "--fix")
	// The `--fix` options is more time consuming (compared to the normal
	// `gx-go uw` call) but as some of the import paths may have been written
	// from synced dependencies (`gx-go link --sync`) of another package that
	// may not be available now (to build the rewrite map) this is the safer
	// option.
	uwcmd.Dir = target
	uwcmd.Stdout = nil
	uwcmd.Stderr = os.Stderr
	if err := uwcmd.Run(); err != nil {
		return "", fmt.Errorf("error during gx-go rw: %s", err)
	}

	// Remove the package at the end as `gx-go rw --fix` will need to use it
	// (to find the DVCS import paths).
	err = os.RemoveAll(filepath.Join(gxSrcDir, "gx", "ipfs", dep.Hash))
	if err != nil {
		return "", fmt.Errorf("error during os.RemoveAll: %s", err)
	}

	return target, nil
}

func GxDvcsImport(pkg *gx.Package) string {
	pkggx := make(map[string]interface{})
	_ = json.Unmarshal(pkg.Gx, &pkggx)
	return pkggx["dvcsimport"].(string)
}
