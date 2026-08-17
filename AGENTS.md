# sing-box fork maintenance contract

This repository is a personal upstream-tracking fork of `SagerNet/sing-box`.
Follow this file for all work under this tree.

## Branch model

- Treat the latest official upstream release tag (currently `v1.14.0-beta.17`) as the production baseline, never an arbitrary development-branch HEAD.
- Keep `upstream/release` as a clean local marker branch pointing exactly at the chosen official release tag.
- Keep upstream-tracking branches (`testing`, `stable`, `oldstable`, `unstable`, `upstream/release`) clean unless the user explicitly says otherwise.
- Do personal work on `cagedbird/...` branches; the current production branch is `cagedbird/alpha`.
- It is acceptable for the personal branch to contain more than one local commit. The invariant is that upstream remains a clean base and local changes are easy to inspect, rebase, drop, or replay.

## Current local feature intent

The maintained local delta is deliberately split into small, independently
auditable patches:

- native outbound providers and subscription parsers, especially Clash links;
- a native `daemon.ProviderService` consumed by Zashboard's `type=singbox` backend;
- remote providers for the hosts DNS transport;
- opt-in Tailscale `force_login` for unattended auth-key enrollment;
- standalone macOS TUN DNS ownership and restoration;
- Linux NetworkManager/systemd recovery integration;
- cagedbird binary, GitHub Release, Homebrew, and Android delivery contracts.

Provider behavior remains core-native, not Android-side conversion. Do not add
wrapper processes, app-layer conversion, or wholesale reF1nd merges. The
provider implementation patch is named `Add native outbound providers for
subscription profiles`; it owns top-level `providers`, registry/manager,
remote/local/inline providers, Clash/sing-box/SIP008/raw parsers, cache restore,
and selector/urltest membership.

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
3. Rebuild or replay the local capability patches on the selected release tag.
   A mechanical rebase or historical cherry-pick is only valid when the old
   release tag is an ancestor of the new tag. Beta release tags may diverge;
   when they do, create a candidate from the new tag and reimplement each
   capability against the new interfaces.
4. Preserve the smallest cagedbird delta. Do not copy whole files from an older
   release when upstream changed them, and do not backport future subsystems to
   satisfy old templates.
5. Verify client gitlinks. `clients/apple` must match the selected upstream tag.
   `clients/android` also matches by default; a different immutable revision is
   allowed only after the tag revision fails a complete APK build and the pinned
   replacement passes it. Record that SHA in `scripts/ci/android-gitlink-override`:
   ```bash
   git ls-tree HEAD clients/android clients/apple
   git ls-tree vX.Y.Z clients/android clients/apple
   scripts/ci/lint-rebase.sh
   ```
6. Verify, then push the personal branch. Use force-with-lease only after a rebase:
   ```bash
   git push --force-with-lease origin cagedbird/alpha
   ```

## Android APK CI

Android release builds use the exact `clients/android` gitlink committed by the
superproject. The selected release tag's gitlink is the default. For beta.17,
the tag revision declares app minSdk 23 while generated `libbox.aar` requires
24, so the pinned compatibility revision raises only the primary flavors to 24.
`scripts/ci/build-android-apks.sh` initializes the submodule recursively and
fails if its checked-out revision differs from the superproject gitlink.

Do not fetch a moving Android `dev` branch during release builds. The Android
application and `experimental/libbox` API evolve in lockstep; combining a
newer Android application with an older release core can fail after the core
library is generated. Recursive initialization remains required for nested
submodules such as `termux-app/terminal-view`.

The `clients/apple` gitlink must remain identical to the selected release tag
even though the cagedbird workflow currently publishes Android artifacts.

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
