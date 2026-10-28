# sing-box

The universal proxy platform.

[![Packaging status](https://repology.org/badge/vertical-allrepos/sing-box.svg)](https://repology.org/project/sing-box/versions)

## Documentation

https://sing-box.sagernet.org

## Android app (cagedbird)

[<img src="https://raw.githubusercontent.com/ImranR98/Obtainium/main/assets/graphics/badge_obtainium.png" alt="Get it on Obtainium" height="54">](https://apps.obtainium.imranr.dev/redirect?r=obtainium://app/%7B%22id%22%3A%22io.nekohasekai.sfa%22%2C%22url%22%3A%22https%3A%2F%2Fgithub.com%2Fcagedbird043%2Fsing-box%22%2C%22author%22%3A%22cagedbird043%22%2C%22name%22%3A%22sing-box%20%28cagedbird%29%22%2C%22additionalSettings%22%3A%22%7B%5C%22includePrereleases%5C%22%3Atrue%2C%5C%22apkFilterRegEx%5C%22%3A%5C%22%5ESFA-.%2A%5C%5C%5C%5C.apk%24%5C%22%2C%5C%22autoApkFilterByArch%5C%22%3Atrue%2C%5C%22appName%5C%22%3A%5C%22sing-box%20%28cagedbird%29%5C%22%7D%22%7D)

Open the badge on a phone with [Obtainium](https://github.com/ImranR98/Obtainium) installed
to add this fork's SFA with updates from its GitHub Releases. The link presets
*Include prereleases* (every cagedbird release is a prerelease), restricts assets to
`SFA-*.apk`, and lets Obtainium pick the APK for the phone's ABI.

The APK keeps the upstream package name `io.nekohasekai.sfa` but is signed with this
fork's key, so an installed official SFA must be uninstalled first. If Obtainium reports
that it cannot reconcile version formats, it switches version detection off and keeps
updating by release tag.

## Private configuration pull (cagedbird)

The puller downloads to a private temporary file, runs `sing-box check`, and only
atomically replaces `config.json` when valid and changed. Download or validation
failures keep the current config; identical content does not restart the service.
The URL is never logged. Keep the URL file root-owned with mode `0600`.

### Linux / Arch Linux

Store the private URL in `/etc/sing-box/config-url`; the target is
`/etc/sing-box/config.json`, matching the service's `/etc/sing-box` config directory.
The package installs `/usr/lib/sing-box/sing-box-config-pull` and
`/usr/lib/systemd/system/sing-box-config-pull.{service,timer}`.

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now sing-box-config-pull.timer
# Optional immediate fetch:
sudo systemctl start sing-box-config-pull.service
```

The timer runs two minutes after boot and every 30 minutes thereafter. A changed
config uses `systemctl try-restart sing-box.service`, so an inactive main service
is not started. Enable the main service separately when ready. `curl` is required.

### macOS / Homebrew

The generated `cagedbird043/tap/sing-box-cagedbird` formula installs the puller
under its `libexec` and a launchd job in its `share/sing-box-cagedbird` directory.
Store the URL at `$(brew --prefix)/etc/sing-box/config-url` (normally
`/opt/homebrew/etc/sing-box/config-url` on Apple Silicon); the target is the adjacent
`config.json`. Start the main Homebrew service before enabling the root-owned
pull job (the formula recommends running the main service as root):

```sh
sudo brew services start sing-box-cagedbird
sudo cp "$(brew --prefix sing-box-cagedbird)/share/sing-box-cagedbird/cn.cagedbird.sing-box-config-pull.plist" /Library/LaunchDaemons/
sudo chown root:wheel /Library/LaunchDaemons/cn.cagedbird.sing-box-config-pull.plist
sudo chmod 644 /Library/LaunchDaemons/cn.cagedbird.sing-box-config-pull.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/cn.cagedbird.sing-box-config-pull.plist
```

The job fetches immediately and every 30 minutes. Changes restart the existing
Homebrew-managed service with `launchctl kickstart -k`, checking the `system`
domain first and then active `gui/<uid>` domains for a user-managed service.
If the service is not loaded in either domain, the puller reports an error.
Logs are in
`$(brew --prefix)/var/log/sing-box-config-pull.log`. To disable it, run
`sudo launchctl bootout system /Library/LaunchDaemons/cn.cagedbird.sing-box-config-pull.plist`.
An absent URL file is a successful, logged skip on either platform.

For isolated testing, paths can be overridden with `SING_BOX_CONFIG_DIR`,
`SING_BOX_CONFIG_URL_FILE`, `SING_BOX_CONFIG_FILE`, and `SING_BOX_BIN`.

## License

```
Copyright (C) 2022 by nekohasekai <contact-sagernet@sekai.icu>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
```

## Agent-native Skills

- [cagedbird-release](.agents/skills/cagedbird-release/SKILL.md): repeatable candidate CI,
  alpha promotion, GitHub Release / Android APK, Homebrew and AUR publication.
  Trigger phrases: “发布 cagedbird”, “打一个 sing-box 个人版”, “更新 Homebrew/AUR”,
  “ship cagedbird release”. The bundled `scripts/update-aur.py <tag>` builds,
  verifies and pushes the AUR wrapper in one command.