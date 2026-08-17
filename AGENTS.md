# sing-box fork maintenance contract

This repository is a personal upstream-tracking fork of `SagerNet/sing-box`.
Follow this file for all work under this tree.

## Branch model

- Treat the latest official upstream release tag (for example `v1.14.0-beta.12`) as the production baseline, never an arbitrary development-branch HEAD.
- Keep `upstream/release` as a clean local marker branch pointing exactly at the chosen official release tag.
- Keep upstream-tracking branches (`testing`, `stable`, `oldstable`, `unstable`, `upstream/release`) clean unless the user explicitly says otherwise.
- Do personal work on `cagedbird/...` branches; the current production branch is `cagedbird/alpha`.
- It is acceptable for the personal branch to contain more than one local commit. The invariant is that upstream remains a clean base and local changes are easy to inspect, rebase, drop, or replay.

## Current local feature intent

The maintained local delta is native core support for subscription providers, especially Clash subscription links.
The intended shape is:

- core-native provider/subscription behavior, not Android-side conversion;
- no wrapper process or app-layer workaround;
- no wholesale merge of `reF1nd/sing-box`;
- selectively port only the provider/subscription closure needed for this feature.

The provider implementation commit in the current floating patch stack is named:

- `Add native outbound providers for subscription profiles`

That patch adds top-level `providers`, provider registry/manager, remote/local/inline providers, Clash/sing-box/SIP008/raw parsers, cache restore, and selector/urltest provider membership.

## Rebase/update procedure

When updating to a new official release tag:

1. Fetch first:
   ```bash
   git fetch upstream origin --tags --prune
   ```
2. Pick the latest official upstream release tag and move only the clean marker branch:
   ```bash
   git branch -f upstream/release vX.Y.Z
   ```
3. Rebase or replay the personal branch on the chosen release tag:
   ```bash
   git switch cagedbird/alpha
   git rebase upstream/release
   ```
4. Resolve conflicts by preserving the smallest cagedbird delta. Do not backport large future-version subsystems into older stable branches just to satisfy current templates.
5. Verify submodule pointers match the upstream tag. `git rebase` does not update gitlinks:
   ```bash
   git ls-tree HEAD clients/android clients/apple
   git ls-tree vX.Y.Z clients/android clients/apple
   # Must be identical. If not: git checkout vX.Y.Z -- clients/android clients/apple
   ```
6. Verify, then push the personal branch. Use force-with-lease only after a rebase:
   ```bash
   git push --force-with-lease origin cagedbird/alpha
   ```

## Android APK CI

Android release builds use the exact `clients/android` gitlink committed by the
selected upstream release tag. `scripts/ci/build-android-apks.sh` initializes
the submodule recursively and fails if its checked-out revision differs from
the superproject gitlink.

Do not fetch a moving Android `dev` branch during release builds. The Android
application and `experimental/libbox` API evolve in lockstep; combining a
newer Android application with an older release core can fail after the core
library is generated. Recursive initialization remains required for nested
submodules such as `termux-app/terminal-view`.

## CI conventions

- Commits that don't change Go source (`**.go`, `go.mod`, `go.sum`, `Makefile`)
  should include `[skip ci]` in the commit message. The core workflow is
  configured with `paths` filters to skip these.
- Release is `workflow_dispatch` only — push to `cagedbird/alpha` does NOT
  trigger a release. Use `gh workflow run cagedbird-release.yml` to release.

## Do not port these from reF1nd unless explicitly requested

- loadbalance outbound;
- pass outbound;
- DNS transport/group rewrites;
- TLS certificate pinning fields;
- proxy protocol;
- broad Clash API restart/update-source behavior;
- unrelated dialer/TCP fields such as `tcp_keep_alive_count`.

## Validation baseline

For provider/core changes, run at least:

```bash
go test ./...
go test -tags with_gvisor ./protocol/tailscale
go build ./cmd/sing-box
scripts/ci/test-provider-core.sh
scripts/ci/lint-rebase.sh
```

Also run a remote-provider smoke when subscription parsing or provider lifecycle changes:

- serve a small Clash YAML subscription over local HTTP;
- configure a `remote` provider with a cache `path`;
- use it from `urltest` or `selector` via `providers: ["sub"]`;
- confirm logs show provider download, cache generation, `sing-box started`, and a generated outbound tag like `sub/local-ss`;
- stop the HTTP server and confirm offline startup from cache still works.


## Commit hygiene

- Keep commits logical and easy to rebase.
- Documentation/process commits like this `AGENTS.md` may be separate from code commits.
- Use the local Lore commit protocol required by the workspace hooks.
- If upstream later adds its own `AGENTS.md`, reconcile rather than blindly overwriting either file.
