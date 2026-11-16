---
name: git-workflow
description: Plan or make commits, branches, history edits, pushes, tags or pull requests in the Proctor repository.
---

# Working with Git in Proctor

The working tree is shared with the owner and sometimes with other agents.
Treat it as someone else's desk.

## Permission

- Looking at the repository is not permission to commit, rewrite, push, tag
  or open a pull request. Each of those needs its own go-ahead.
- A fetch authorizes nothing.

## Hygiene

- Preserve changes you didn't make: unrelated modifications, untracked files
  and staged state.
- Stage by explicit path or hunk, never with a blanket add over a tree you
  didn't inspect.
- Never commit `go.work.sum`.
- Never use a bare `git stash` in a shared tree.

## Commits

- **One commit, one reason.** Code, tests, OpenAPI, docs, migrations and
  generated output for that reason travel together. Partition by intent, not
  by file type. If two changes can't be separated safely, say so rather than
  forcing it.
- **Subject:** `<type>[(<scope>)]: <imperative outcome>`, lowercase, with no
  period and at most 72 characters.
  - Types: `feat`, `fix`, `refactor`, `docs`, `test`, `perf`, `build`, `ci`,
    `chore`.
  - Scopes: `server`, `webapp`, `docs`, `cache`, `mail`, `vfs`, `tooling`.
- **Body:** say why, and any non-obvious mechanism, compatibility, security or
  migration effect. Don't list the files.
- **Breaking changes** use `!` in the subject and a `BREAKING CHANGE:` footer.
- **Every commit is signed off** (`git commit -s`, the DCO trailer). Run
  `git diff --cached --check` before committing.

## History and remotes

- Prefer a new commit to fix shared history.
- Rewrite only when asked, and then push with `--force-with-lease` after
  refreshing the remote tip.

## Pull requests

- One outcome per pull request, titled for the net result.
- Follow `.github/pull_request_template.md`. Write the description from the
  whole `<base>...HEAD` diff and the checks you actually ran.
- Add reviewers, labels or auto-merge only when asked.
