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

Example workflow:

> gx-go link QmQA5mdxru8Bh6dpC9PJfSkumqnmHgJX7knxSgBo5Lpime QmVGtdTZdTFaLsaj2RwdVG8jcjNNcp1DE914DKZ2kHmXHw QmSpJByNKFX1sCsHBEp3R73FL4NF6FnQTEGyNAXHm2GS52
linked QmQA5mdxru8Bh6dpC9PJfSkumqnmHgJX7knxSgBo5Lpime /home/user/go/src/github.com/libp2p/go-libp2p
linked QmVGtdTZdTFaLsaj2RwdVG8jcjNNcp1DE914DKZ2kHmXHw /home/user/go/src/github.com/multiformats/go-multihash
linked QmSpJByNKFX1sCsHBEp3R73FL4NF6FnQTEGyNAXHm2GS52 /home/user/go/src/github.com/ipfs/go-log

> gx-go link
QmQA5mdxru8Bh6dpC9PJfSkumqnmHgJX7knxSgBo5Lpime /home/user/go/src/github.com/libp2p/go-libp2p
QmSpJByNKFX1sCsHBEp3R73FL4NF6FnQTEGyNAXHm2GS52 /home/user/go/src/github.com/ipfs/go-log
QmVGtdTZdTFaLsaj2RwdVG8jcjNNcp1DE914DKZ2kHmXHw /home/user/go/src/github.com/multiformats/go-multihash

> gx-go link -r QmSpJByNKFX1sCsHBEp3R73FL4NF6FnQTEGyNAXHm2GS52
unlinked QmSpJByNKFX1sCsHBEp3R73FL4NF6FnQTEGyNAXHm2GS52 /home/user/go/src/github.com/ipfs/go-log

> gx-go link
QmQA5mdxru8Bh6dpC9PJfSkumqnmHgJX7knxSgBo5Lpime /home/user/go/src/github.com/libp2p/go-libp2p
QmVGtdTZdTFaLsaj2RwdVG8jcjNNcp1DE914DKZ2kHmXHw /home/user/go/src/github.com/multiformats/go-multihash

> gx-go link -r -a
unlinked QmQA5mdxru8Bh6dpC9PJfSkumqnmHgJX7knxSgBo5Lpime /home/user/go/src/github.com/libp2p/go-libp2p
unlinked QmVGtdTZdTFaLsaj2RwdVG8jcjNNcp1DE914DKZ2kHmXHw /home/user/go/src/github.com/multiformats/go-multihash

> gx-go link
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
			Name:  "w,write-imports",
			Usage: "Re-write or un-rewrite import paths",
		},
	},
	Action: func(c *cli.Context) error {
		remove := c.Bool("remove")
		all := c.Bool("all")
		writeImports := c.Bool("write-imports")

		hashes := c.Args()[:]
		if len(hashes) == 0 {
			links, err := listLinkedPackages()
			if err != nil {
				return err
			}

			if remove && all {
				for _, link := range links {
					hashes = append(hashes, link[0])
				}
			}

			if !remove {
				for _, link := range links {
					fmt.Printf("%s %s\n", link[0], link[1])
				}
				return nil
			}
		}

		for _, hash := range hashes {
			if remove {
				target, err := unlinkPackage(hash, writeImports)
				if err != nil {
					return err
				}
				fmt.Printf("unlinked %s %s\n", hash, target)
			} else {
				target, err := linkPackage(hash, writeImports)
				if err != nil {
					return err
				}
				fmt.Printf("linked %s %s\n", hash, target)
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

// gx get $hash
// go get $dvcsimport
// rm -rf $GOPATH/src/gx/ipfs/$hash/$pkgname
// ln -s $GOPATH/src/$dvcsimport $GOPATH/src/gx/ipfs/$hash/$pkgname
// cd $GOPATH/src/$dvcsimport && gx install && gx-go rewrite
func linkPackage(hash string, writeImports bool) (string, error) {
	srcdir, err := gx.InstallPath("go", "", true)
	if err != nil {
		return "", err
	}
	gxdir := filepath.Join(srcdir, "gx", "ipfs", hash)

	gxget := exec.Command("gx", "get", hash, "-o", gxdir)
	gxget.Stdout = os.Stderr
	gxget.Stderr = os.Stderr
	if err = gxget.Run(); err != nil {
		return "", fmt.Errorf("error during gx get: %s", err)
	}

	var pkg gx.Package
	err = gx.FindPackageInDir(&pkg, gxdir)
	if err != nil {
		return "", fmt.Errorf("error during gx.FindPackageInDir: %s", err)
	}

	dvcsimport := GxDvcsImport(&pkg)
	target := filepath.Join(srcdir, dvcsimport)
	gxtarget := filepath.Join(gxdir, pkg.Name)

	_, err = os.Stat(target)
	if err == os.ErrNotExist {
		goget := exec.Command("go", "get", dvcsimport+"/...")
		goget.Stdout = nil
		goget.Stderr = os.Stderr
		if err = goget.Run(); err != nil {
			return "", fmt.Errorf("error during go get: %s", err)
		}
	} else if err != nil {
		return "", fmt.Errorf("error during os.Stat: %s", err)
	}

	err = os.RemoveAll(gxtarget)
	if err != nil {
		return "", fmt.Errorf("error during os.RemoveAll: %s", err)
	}

	err = os.Symlink(target, gxtarget)
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

	if writeImports {
		rwcmd := exec.Command("gx-go", "rw")
		rwcmd.Dir = target
		rwcmd.Stdout = nil
		rwcmd.Stderr = os.Stderr
		if err := rwcmd.Run(); err != nil {
			return "", fmt.Errorf("error during gx-go rw: %s", err)
		}
	}

	return target, nil
}

// rm -rf $GOPATH/src/gx/ipfs/$hash
// gx get $hash
func unlinkPackage(hash string, writeImports bool) (string, error) {
	srcdir, err := gx.InstallPath("go", "", true)
	if err != nil {
		return "", err
	}
	gxdir := filepath.Join(srcdir, "gx", "ipfs", hash)

	err = os.RemoveAll(gxdir)
	if err != nil {
		return "", fmt.Errorf("error during os.RemoveAll: %s", err)
	}

	gxget := exec.Command("gx", "get", hash, "-o", gxdir)
	gxget.Stdout = nil
	gxget.Stderr = os.Stderr
	if err = gxget.Run(); err != nil {
		return "", fmt.Errorf("error during gx get: %s", err)
	}

	var pkg gx.Package
	err = gx.FindPackageInDir(&pkg, gxdir)
	if err != nil {
		return "", fmt.Errorf("error during gx.FindPackageInDir: %s", err)
	}

	dvcsimport := GxDvcsImport(&pkg)
	target := filepath.Join(srcdir, dvcsimport)

	if writeImports {
		uwcmd := exec.Command("gx-go", "uw")
		uwcmd.Dir = target
		uwcmd.Stdout = nil
		uwcmd.Stderr = os.Stderr
		if err := uwcmd.Run(); err != nil {
			return "", fmt.Errorf("error during gx-go uw: %s", err)
		}
	}

	return target, nil
}

func GxDvcsImport(pkg *gx.Package) string {
	pkggx := make(map[string]interface{})
	_ = json.Unmarshal(pkg.Gx, &pkggx)
	return pkggx["dvcsimport"].(string)
}
