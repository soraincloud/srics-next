"""Check network failure handling against a synthetic macOS background service.

Uses a temporary library, a random loopback port and its own launchd label.
Never opens the user's default configuration or changes network interfaces.
"""
import hashlib
import json
import os
import pathlib
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import urllib.request

source_binary = pathlib.Path(sys.argv[1]).resolve()
if sys.platform != "darwin":
    sys.exit("This check requires macOS launchd.")

with tempfile.TemporaryDirectory(prefix="srics-network-lifecycle-") as temporary:
    root = pathlib.Path(temporary)
    # launchd must be able to execute the fixture independently of Documents
    # folder privacy grants belonging to the foreground build application.
    binary = root / "srics-network-check"
    shutil.copy2(source_binary, binary)
    if (source_binary.parent / "tools").is_dir():
        (root / "tools").symlink_to(source_binary.parent / "tools", target_is_directory=True)
    path = root / "config.json"
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    config = dict(data=str(root / "library"), port=port, lanAddress="")
    password = "synthetic-network-login-password"
    env = dict(os.environ, PATH="/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin")
    stopped_pid = None

    def manager(action, body=None, failure=False):
        result = subprocess.run(
            [str(binary), "manager", action, "--config", str(path)],
            input=json.dumps(body) if body is not None else "",
            text=True, capture_output=True, env=env, timeout=35,
        )
        if failure:
            assert result.returncode != 0, f"{action} unexpectedly succeeded"
            return result.stderr
        if result.returncode != 0:
            log = root / "service.log"
            details = log.read_text()[-8192:] if log.exists() else ""
            raise AssertionError(result.stderr + "\nSynthetic service log:\n" + details)
        return json.loads(result.stdout)

    def login():
        client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        request = urllib.request.Request(
            f"http://127.0.0.1:{port}/api/auth/login",
            data=json.dumps(dict(password=password)).encode(),
            headers={"X-SRICS-Request": "app", "Content-Type": "application/json"},
        )
        with client.open(request, timeout=3) as response:
            assert json.load(response)["authenticated"]

    try:
        manager("configure", dict(config=config, password=password,
                                 vaultPassword="synthetic-network-vault-password",
                                 vaultIdleMinutes=7))
        for _ in range(2):
            assert manager("start")["running"]
            assert manager("start")["passwordSet"]
            login()
            assert not manager("stop")["running"]
            assert not manager("stop")["running"]

        with socket.socket() as blocker:
            blocker.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            blocker.bind(("127.0.0.1", port))
            blocker.listen()
            error = json.loads(manager("start", failure=True))
            assert error["field"] == "port", error

        assert manager("start")["running"]
        target = f"gui/{os.getuid()}/com.soraincloud.srics." + hashlib.sha256(str(path).encode()).hexdigest()
        state = subprocess.check_output(["/bin/launchctl", "print", target], text=True)
        match = re.search(r"\bpid = (\d+)", state)
        assert match, "synthetic service did not have a PID"
        stopped_pid = int(match.group(1))
        os.kill(stopped_pid, signal.SIGSTOP)
        unhealthy = manager("info")
        assert unhealthy["running"] and unhealthy["serviceError"], unhealthy
        error = manager("start", failure=True)
        assert "响应检查" in error and "请先保存" not in error, error
        os.kill(stopped_pid, signal.SIGCONT)
        stopped_pid = None
        assert not manager("stop")["running"]
        stopped = manager("info")
        assert stopped["passwordSet"] and stopped["vaultSet"] and stopped["vaultIdleMinutes"] == 7

        # A valid private IP absent from this Mac models an old DHCP lease.
        unavailable = next(f"192.168.7.{n}" for n in range(1, 255)
                           if f"192.168.7.{n}" not in stopped["lanAddresses"])
        manager("configure", dict(config={**config, "lanAddress": unavailable}, vaultIdleMinutes=7))
        stale = manager("info")
        assert stale["networkError"] and stale["passwordSet"] and stale["vaultSet"]
        error = json.loads(manager("start", failure=True))
        assert error["field"] == "lanAddress" and "端口" not in error["message"], error
        manager("configure", dict(config=config, vaultIdleMinutes=7))
        assert manager("start")["passwordSet"]
        login()
        assert not manager("stop")["running"]
        assert password not in (root / "service.log").read_text()
        print("PASS: repeated start/stop + login, genuine port conflict, stalled live process diagnosed without requesting new passwords, stale address distinguished, credentials retained after recovery")
    finally:
        if stopped_pid is not None:
            os.kill(stopped_pid, signal.SIGCONT)
        manager("stop")
