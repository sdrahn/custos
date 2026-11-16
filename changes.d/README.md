# changes.d: the changes since the last release

A change that users notice gets its changelog entry here, as a file of
its own, in the pull request that makes the change. `CHANGELOG.md`
itself is written only at a release, so two pull requests never change
the same lines of it.

## A fragment

`changes.d/<topic>.<kind>.md`, for example
`changes.d/compare-exit-status.fixed.md`:

```markdown
- `agent-admin compare` exits with 1 when an agent differs from its
  template, where it exited with 0 before.
```

- `<topic>`: lower-case letters, digits and `-`; the branch's topic
  does well.
- `<kind>`: `added`, `changed`, `fixed`, `removed` or `security`, the
  heading the entry goes under.
- The content: one or more list items, written as they will stand in
  `CHANGELOG.md`: `- ` for an item, two spaces for its further lines.
  Say what changes for someone who uses Custos, and what was there
  before.

A pull request with entries of two kinds adds two files. A change
nobody notices from outside (a refactoring, a test) needs none.

## At a release

`changes.d/intro.md`, written for the release, is its introduction:
what the release is about and what to know when upgrading.

```bash
tools/changelog show                    # the section as it stands
tools/changelog release vX.Y.Z [DATE]   # into CHANGELOG.md; removes the fragments
```

`tools/changelog check` says whether every file here is a fragment; the
tests run it (`make check`), and `tools/check-release` refuses a commit
that still has fragments.

## A fix release

A fix that goes from main to a release branch takes its fragment
along. The release commit on the branch runs `tools/changelog release`
there. Main then gets the same section: run the same command on main
(the fragment is still there), in the pull request that follows the
release.
