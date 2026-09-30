"""Exercise the packaged background service using an isolated synthetic library."""
import hashlib
import time
import re
import signal
import http.cookiejar
import json
import os
import pathlib
import socket
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request

binary = pathlib.Path(sys.argv[1]).resolve()
with tempfile.TemporaryDirectory(prefix='srics-launcher-test-') as temporary:
    root = pathlib.Path(temporary)
    config_path = root / 'config.json'
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    config = dict(data=str(root / 'library'), port=port, backupRepository='', backupPasswordFile='', autoRestart=True)
    request = dict(config=config, password='synthetic-launcher-test-password', currentPassword='')
    env = dict(os.environ, PATH='/usr/bin:/bin:/usr/sbin:/sbin')
    def manager(action, body=None, success=True):
        args = [str(binary), 'manager', action, '--config', str(config_path)]
        if action == 'start' and body is not None:
            args.append('--verify-login')
        result = subprocess.run(args, input=json.dumps(body) if body else '', text=True, capture_output=True, env=env, timeout=35)
        if success:
            assert result.returncode == 0, result.stderr
            return json.loads(result.stdout)
        assert result.returncode != 0, f'{action} unexpectedly succeeded'
    jar = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(jar))
    def api(path, body=None):
        payload = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(f'http://127.0.0.1:{port}'+path, data=payload, headers={'X-SRICS-Request':'app','Content-Type':'application/json'})
        with client.open(req, timeout=3) as response:
            return json.load(response)
    try:
        manager('start', success=False)
        assert manager('configure', request)['passwordSet']
        manager('start', {'password': 'incorrect-synthetic-password'}, success=False)
        assert not manager('info')['running'], 'failed login verification left the service running'
        with socket.socket() as blocker:
            blocker.bind(('127.0.0.1', port)); blocker.listen()
            manager('start', success=False)
        verified = manager('start', {'password': request['password']})
        assert verified['running'] and verified['loginVerified'], 'first configured password was not verified'
        assert manager('start')['running'], 'repeated start failed'
        assert manager('info')['running'], 'service did not outlive start command'
        target = f'gui/{os.getuid()}/com.soraincloud.srics.' + hashlib.sha256(str(config_path).encode()).hexdigest()
        def pid():
            output = subprocess.run(['/bin/launchctl', 'print', target], capture_output=True, text=True, check=True).stdout
            match = re.search(r'\bpid = (\d+)', output)
            return int(match.group(1)) if match else None
        original_pid = pid()
        assert original_pid, 'missing owned test service pid'
        os.kill(original_pid, signal.SIGKILL)
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            state = manager('info')
            if pid() not in (None, original_pid) and state['running'] and state['passwordSet']:
                break
            time.sleep(1)
        else:
            raise AssertionError('test service did not restart after a crash')
        assert api('/api/auth')['configured']
        try:
            api('/api/auth/setup', {'password':'replacement-password'})
            raise AssertionError('web setup is still available')
        except urllib.error.HTTPError as error:
            assert error.code == 404
        assert api('/api/auth/login', {'password':request['password']})['authenticated']
        assert api('/api/auth')['authenticated']
        manager('configure', request, success=False)
        assert not manager('stop')['running']
        assert not manager('stop')['running'], 'repeated stop failed'
        assert manager('start')['running']
        assert not api('/api/auth')['authenticated'], 'old session survived restart'
        assert api('/api/auth/login', {'password':request['password']})['authenticated']
        assert not manager('stop')['running']
        assert request['password'] not in config_path.read_text()
        assert request['password'] not in (root / 'service.log').read_text()
        old_password = request['password']
        request.update(currentPassword=old_password, password=' 测试-cafe\u0301-"password"-🔑 ')
        assert manager('configure', request)['passwordSet']
        assert manager('start', {'password': request['password']})['loginVerified']
        assert api('/api/auth/login', {'password': request['password']})['authenticated']
        try:
            api('/api/auth/login', {'password': old_password})
            raise AssertionError('old password remained valid after a saved change')
        except urllib.error.HTTPError as error:
            assert error.code == 401
        assert not manager('stop')['running']
        assert request['password'] not in config_path.read_text()
        assert request['password'] not in (root / 'service.log').read_text()
        backup_password = root / 'backup-password.txt'
        backup_password.write_text('synthetic-update-password'); backup_password.chmod(0o600)
        config.update(backupRepository=str(root / 'backup'), backupPasswordFile=str(backup_password))
        request['password'] = ''; manager('configure', request)
        restic = binary.parent / 'tools' / 'restic'
        subprocess.run([str(restic), '--repo', config['backupRepository'], 'init'], env=dict(env, RESTIC_PASSWORD='synthetic-update-password'), check=True, capture_output=True)
        assert manager('start')['running']
        prepared = manager('prepare-update')
        assert not prepared['running'], 'update preparation left service running'
        update_dir = pathlib.Path(prepared['updateBackup'])
        record = json.loads((update_dir / 'update.json').read_text())
        assert len(record['snapshot']) == 64 and pathlib.Path(record['previousApp']).is_dir()
        assert (update_dir / 'config.json').stat().st_mode & 0o777 == 0o600
        assert manager('start')['running'], 'could not restart after update preparation'
        print('PASS: first setup + verified login, verification failure stops service, Unicode password change + old password rejected, crash restart, update backup + retained app + restart, port conflict, background lifetime, duplicate start/stop, web setup removal, configuration lock, session invalidation')
    finally:
        manager('stop')
