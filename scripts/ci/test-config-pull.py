#!/usr/bin/env python3
"""Exercise the real config puller and sing-box check without touching host services."""
import functools
import http.server
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading


def main():
    binary = str(Path(sys.argv[1]).resolve())
    script = Path(__file__).resolve().parents[2] / "release/config/sing-box-config-pull"
    with tempfile.TemporaryDirectory(prefix="sing-box-pull-test-") as directory:
        root = Path(directory)
        web = root / "web"
        web.mkdir()
        config = root / "config"
        config.mkdir()
        commands = root / "bin"
        commands.mkdir()
        restart_log = root / "restarts"
        systemctl = commands / "systemctl"
        systemctl.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "$RESTART_LOG"\n')
        systemctl.chmod(0o755)
        handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=web)
        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        env = dict(os.environ, SING_BOX_CONFIG_DIR=str(config), SING_BOX_BIN=binary,
                   PATH=f"{commands}:{os.environ['PATH']}", RESTART_LOG=str(restart_log))
        def run(success=True):
            result = subprocess.run([str(script)], env=env, capture_output=True, text=True)
            assert (result.returncode == 0) == success, result
            assert "http://" not in result.stdout + result.stderr, "private URL leaked"
            print((result.stdout + result.stderr).strip())
        try:
            run()  # Installed, but not configured.
            payload = web / "config"
            valid = '{"outbounds":[{"type":"direct","tag":"direct"}]}\n'
            payload.write_text(valid)
            (config / "config.json").write_text('{}\n')
            (config / "config-url").write_text(f"http://127.0.0.1:{server.server_port}/config")
            run()
            assert (config / "config.json").read_text() == valid
            assert restart_log.read_text() == "try-restart sing-box.service\n"
            inode = (config / "config.json").stat().st_ino
            run()
            assert (config / "config.json").stat().st_ino == inode
            assert len(restart_log.read_text().splitlines()) == 1
            payload.write_text('{"unknown_option":true}\n')
            run(False)
            assert (config / "config.json").read_text() == valid
            payload.unlink()
            run(False)
            assert (config / "config.json").read_text() == valid
            assert len(restart_log.read_text().splitlines()) == 1
            assert not list(config.glob(".config-pull.*"))
            print("PASS: missing URL, valid replacement, identical no-op, invalid/download failure preservation")
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    main()
