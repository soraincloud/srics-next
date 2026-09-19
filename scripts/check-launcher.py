"""Exercise the packaged background service using an isolated synthetic library."""
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
    config = dict(data=str(root / 'library'), port=port, backupRepository='', backupPasswordFile='')
    request = dict(config=config, password='synthetic-launcher-test-password', currentPassword='')
    env = dict(os.environ, PATH='/usr/bin:/bin:/usr/sbin:/sbin')
    def manager(action, body=None, success=True):
        result = subprocess.run([str(binary), 'manager', action, '--config', str(config_path)], input=json.dumps(body) if body else '', text=True, capture_output=True, env=env, timeout=35)
        if success:
            assert result.returncode == 0, result.stderr
            return json.loads(result.stdout)
        assert result.returncode != 0, f'{action} unexpectedly succeeded'
    jar = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    def api(path, body=None):
        payload = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(f'http://127.0.0.1:{port}'+path, data=payload, headers={'X-SRICS-Request':'app','Content-Type':'application/json'})
        with client.open(req, timeout=3) as response:
            return json.load(response)
    try:
        manager('start', success=False)
        assert manager('configure', request)['passwordSet']
        with socket.socket() as blocker:
            blocker.bind(('127.0.0.1', port)); blocker.listen()
            manager('start', success=False)
        assert manager('start')['running']
        assert manager('start')['running'], 'repeated start failed'
        assert manager('info')['running'], 'service did not outlive start command'
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
        print('PASS: local setup, port conflict, background lifetime, duplicate start/stop, web setup removal, login, configuration lock, session invalidation')
    finally:
        manager('stop')
