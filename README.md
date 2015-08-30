# gx-go-import

import a go package and all its dependencies into gx package management.

## Usage
```
$ gx-go-import github.com/whyrusleeping/hellabot
```

## NOTE:
It is highly recommended that you set your `GOPATH` to a temporary directory when running this command.
This ensures that your current go packages are not affected, and also that fresh versions of
the packages in question are pulled down.
