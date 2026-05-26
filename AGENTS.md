# sing-box fork maintenance contract

This repository is a personal upstream-tracking fork of `SagerNet/sing-box`.
Follow this file for all work under this tree.

## Branch model

- Treat `upstream/testing` as the clean upstream baseline.
- Keep local upstream-tracking branches (`testing`, `stable`, `oldstable`, `unstable`) clean unless the user explicitly says otherwise.
- Do personal work on `cagedbird/...` branches; the current feature branch is `cagedbird/feature-base`.
- It is acceptable for the personal branch to contain more than one local commit. The invariant is that upstream remains a clean base and local changes are easy to inspect, rebase, drop, or replay.

## Current local feature intent

The maintained local delta is native core support for subscription providers, especially Clash subscription links.
The intended shape is:

- core-native provider/subscription behavior, not Android-side conversion;
- no wrapper process or app-layer workaround;
- no wholesale merge of `reF1nd/sing-box`;
- selectively port only the provider/subscription closure needed for this feature.

The main provider implementation commit currently rebased on upstream is:

- `666bdb5c Add native outbound providers for subscription profiles`

That commit adds top-level `providers`, provider registry/manager, remote/local/inline providers, Clash/sing-box/SIP008/raw parsers, cache restore, and selector/urltest provider membership. Other local commits add cagedbird CI/release automation, Android app tracking, hosts providers, and local Tailscale UI/API helpers.

## Rebase/update procedure

When updating to a new upstream `testing`:

1. Fetch first:
   ```bash
   git fetch upstream origin --prune
   git submodule update --init --recursive clients/android
   ```
2. Keep upstream-tracking branches (`testing`, `stable`, `oldstable`, `unstable`) aligned with upstream and unmodified. Do not commit personal changes there.
3. Rebase the personal branch on the new baseline:
   ```bash
   git switch cagedbird/feature-base
   git rebase upstream/testing
   ```
4. Resolve conflicts by preserving the smallest native provider delta. Do not re-import unrelated `reF1nd` features.
5. Always sync the Android submodule after a core rebase or upstream alpha bump:
   ```bash
   CAGEDBIRD_PUSH_ANDROID_BRANCH=1 scripts/ci/sync-android-submodule.sh
   git add clients/android
   ```
   This rebases `clients/android` branch `fix/per-app-proxy-vpn-builder` onto official Android `dev`, runs the sync check, optionally force-pushes the Android fork branch, and updates the parent gitlink.
6. Verify before pushing the parent branch:
   ```bash
   scripts/ci/check-android-submodule-sync.sh
   go test -tags 'with_gvisor with_tailscale with_clash_api' ./protocol/tailscale ./experimental/clashapi ./dns/transport/hosts
   ```
7. Push the personal branch. Use force-with-lease only after a rebase:
   ```bash
   git push --force-with-lease origin cagedbird/feature-base
   ```


## Android submodule rule

`clients/android` is intentionally pointed at the cagedbird Android fork branch `fix/per-app-proxy-vpn-builder`, not directly at upstream Android `dev`. The parent repository stores a gitlink, so CI will fail if the Android fork branch contains an older upstream baseline than the core release baseline.

Do not manually guess or hand-edit the Android submodule pointer. Use:

```bash
scripts/ci/sync-android-submodule.sh
```

Use this when CI reports classic submodule pointer drift, after any upstream `testing` rebase, or after a new upstream alpha tag. Use `CAGEDBIRD_PUSH_ANDROID_BRANCH=1` when the rebased Android fork branch should be pushed. Commit the resulting parent `clients/android` gitlink update.

## Do not port these from reF1nd unless explicitly requested

- loadbalance outbound;
- pass outbound;
- DNS transport/group rewrites;
- TLS certificate pinning fields;
- proxy protocol;
- Android update-source changes;
- broad Clash API restart/update-source behavior;
- unrelated dialer/TCP fields such as `tcp_keep_alive_count`.

## Validation baseline

For provider/core changes, run at least:

```bash
go test ./adapter/provider ./provider/parser ./provider/remote ./provider/local ./protocol/group ./option
go list ./... | grep -v '^github.com/sagernet/sing-box/experimental/libbox$' | xargs go test
go build ./cmd/sing-box
```

For cagedbird Tailscale/Clash API helpers, run at least:

```bash
go test -tags 'with_gvisor with_tailscale with_clash_api' ./protocol/tailscale ./experimental/clashapi
```

Also run a remote-provider smoke when subscription parsing or provider lifecycle changes:

- serve a small Clash YAML subscription over local HTTP;
- configure a `remote` provider with a cache `path`;
- use it from `urltest` or `selector` via `providers: ["sub"]`;
- confirm logs show provider download, cache generation, `sing-box started`, and a generated outbound tag like `sub/local-ss`;
- stop the HTTP server and confirm offline startup from cache still works.

Known caveat at the time this file was added:

```text
go test ./...
```

fails only at `experimental/libbox` link time with:

```text
invalid reference to runtime/pprof.parseProcSelfMaps
```

Do not treat that existing libbox/toolchain link issue as evidence that the provider core patch is broken unless new evidence points there.

## Commit hygiene

- Keep commits logical and easy to rebase.
- Documentation/process commits like this `AGENTS.md` may be separate from code commits.
- Use the local Lore commit protocol required by the workspace hooks.
- If upstream later adds its own `AGENTS.md`, reconcile rather than blindly overwriting either file.
