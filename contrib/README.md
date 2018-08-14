# Scripts to handle common GX-go tasks

## gx-rebase

`gx-rebase` performs a `git rebase` on a git repository with gx-rewritten paths,
transparently fixing these paths.

Usage:

```sh
> ./gx-retrotag.sh [[base] new-branch]
```

By default, `base` is `master` and `new-branch` is `${current_branch}-fixed`.

**Warning:** This command can't handle other conflicts automatically. You'll be
dropped back into your shell where you will need to:

1. Fix the conflicts
2. Add the files to the git index.
3. Run gx-rebase (no arguments) and hit `y` at the continue prompt.
