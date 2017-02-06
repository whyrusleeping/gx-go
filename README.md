# gx-go

A subtool for the gx package manager for packages written in go.

## Usage:
```
NAME:
   gx-go - gx extensions for golang

USAGE:
   gx-go [global options] command [command options] [arguments...]

VERSION:
   1.3.0

AUTHOR(S):
   whyrusleeping

COMMANDS:
     dep-map      prints out a json dep map for usage by 'import --map'
     hook         go specific hooks to be called by the gx tool
     import       import a go package and all its depencies into gx
     path         prints the import path of the current package within GOPATH
     rewrite, rw  temporary hack to evade causality
     uw
     update       update a packages imports to a new path
     dvcs-deps    display dvcs deps that arent tracked in gx
     get          gx-ified `go get`

GLOBAL OPTIONS:
   --verbose      turn on verbose output
   --help, -h     show help
   --version, -v  print the version
```

## Intro
Using gx as a go vendoring tool and package manager is (or at least, should be) a
very simple process.

### Creating a new package
In the directory of your go package, just run:
```
gx init --lang=go
```

And gx will create a new `package.json` for you with some basic information
filled out. From there, all you *have* to do is run `gx publish` (ensure you
have a running ipfs daemon) and gx will give you a package hash. That works
fine for the base case, but to work even more nicely with go, we recommend
setting the import path of your package in your `package.json`, like so:

package.json
```json
{
	...
	"gx":{
		"dvcsimport":"github.com/whyrusleeping/gx-go"
	}
}
```

If you're initializing a new gx package from the appropriate location within
your `GOPATH`, `gx-go` will attempt to pre-fill the dvcsimport field for you
automatically.

### Importing an existing package
Importing an existing go package from gx is easy, just grab its hash from
somewhere, and run:
```
gx import <thathash>
```

If the package you are importing has its dvcs import path set as shown above,
gx will ask if you want to rewrite your import paths with the new gx path.
If you say no to this (as is the default), you can rewrite the paths at any time
by running `gx-go rewrite`.

### Some notes on publishing
It is recommended that when you publish, your import paths are *not* rewritten.
The gx-go post install hook will fix that after the install, but for 'same package'
imports, it works best to have gx rewrite things after the fact (Its also sometimes
nicer for development). You can change paths back from their gx paths with:
```
gx-go rewrite --undo
```

A few other notes:

- When publishing, make sure that you don't have any duplicate dependencies
  (different hash versions of the same package). You can check this with `gx
  deps dupes`
- Make sure that you arent missing any dependencies, With your dependencies
  written in gx form, run `gx-go dvcs-deps`. If it outputs any package that is
  not the package you are publishing, you should probably look at importing
  that package to gx as well.
- Make sure the tests pass with gx rewritten deps. `gx test` will write gx deps
  and run `go test` for you.

## NOTE:
It is highly recommended that you set your `GOPATH` to a temporary directory when running import.
This ensures that your current go packages are not affected, and also that fresh versions of
the packages in question are pulled down.
