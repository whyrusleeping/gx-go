# gx-go-tool

A tool to use with the gx package manager for packages written in go.

## Usage:
```
NAME:
   gx-go - gx extensions for golang

USAGE:
   gx-go [global options] command [command options] [arguments...]
   
VERSION:
   0.2.0
   
AUTHOR(S):
   whyrusleeping 
   
COMMANDS:
   update	update a packages imports to a new path
   import	import a go package and all its depencies into gx
   path		prints the import path of the current package within GOPATH
   hook		go specific hooks to be called by the gx tool
   help, h	Shows a list of commands or help for one command
   
GLOBAL OPTIONS:
   --help, -h		show help
   --version, -v	print the version
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

## NOTE:
It is highly recommended that you set your `GOPATH` to a temporary directory when running import.
This ensures that your current go packages are not affected, and also that fresh versions of
the packages in question are pulled down.
