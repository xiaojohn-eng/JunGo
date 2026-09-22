#!/usr/bin/env python3
"""Isolated loopback control/device/HTTP fixture for Android emulator QA.
No user device credentials, cloud services, or real shared directories are used.
"""
import hashlib, http.server, json, pathlib, signal, subprocess, sys, threading, time

ROOT = pathlib.Path(__file__).resolve().parents[1]
STATE = ROOT / '.state' / 'android-qa'
STATE.mkdir(parents=True, exist_ok=True, mode=0o700)
BIN = str(ROOT / 'dist' / 'jungo')
children, logs = [], []

def start(name, args):
    log = (STATE / (name + '.log')).open('a')
    logs.append(log)
    child = subprocess.Popen([BIN] + args, stdout=log, stderr=log)
    children.append(child)
    return child

def rpc(method, params=None):
    result = subprocess.run([BIN, 'rpc', '--state', str(STATE/'device')], input=json.dumps({'method':method,'params':params or {}}), text=True, capture_output=True, check=True)
    return json.loads(result.stdout)

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        data = b'JunGo VPN QA: private HTTP reached through WireGuard\n'
        self.send_response(200)
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)
    def log_message(self, *args): pass

def stop(*args):
    raise KeyboardInterrupt

signal.signal(signal.SIGTERM, stop)
try:
    server = http.server.ThreadingHTTPServer(('127.0.0.1',9081), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    start('control', ['serve','--state',str(STATE/'control'),'--listen','127.0.0.1:9444','--stun','127.0.0.1:3478'])
    start('device', ['agent','--state',str(STATE/'device'),'--local-api'])
    for _ in range(100):
        if any(c.poll() is not None for c in children): raise RuntimeError('QA child failed; inspect private .state/android-qa logs')
        try:
            current = rpc('state')
            if (STATE/'control/admin.token').exists(): break
        except (subprocess.CalledProcessError,FileNotFoundError,json.JSONDecodeError): pass
        time.sleep(.1)
    else: raise RuntimeError('QA fixture timed out')
    def pairing():
        return json.loads(subprocess.check_output([BIN,'pair-code','--state',str(STATE/'control'),'--server','https://127.0.0.1:9444'],text=True))
    if not current['paired']:
        p = pairing()
        p['name'] = 'QA Mac'
        rpc('pair', p)
    share = STATE/'shared'; share.mkdir(exist_ok=True)
    (share/'proof.txt').write_text('JunGo authenticated file QA\n')
    if not rpc('state')['shares']: rpc('shareAdd',{'name':'QA files','path':str(share),'readOnly':False})
    if not rpc('state')['services']: rpc('serviceAdd',{'port':9080,'target':'127.0.0.1:9081','network':'tcp'})
    state = rpc('network',{'mesh':True})
    p = pairing(); p['server']='https://10.0.2.2:9444'
    payload = STATE/'android-pairing.json'
    payload.write_text(json.dumps(p)); payload.chmod(0o600)
    public = {'ip':state['device']['ip'],'hostname':state['device']['hostname'],'port':9080,'pairingFile':str(payload)}
    (STATE/'fixture.json').write_text(json.dumps(public)); (STATE/'fixture.json').chmod(0o600)
    print(json.dumps(public),flush=True)
    while True:
        if any(c.poll() is not None for c in children): raise RuntimeError('QA child stopped')
        time.sleep(1)
except KeyboardInterrupt:
    pass
finally:
    for child in children:
        if child.poll() is None: child.terminate()
    for child in children:
        try: child.wait(timeout=8)
        except subprocess.TimeoutExpired: child.kill(); child.wait()
    for log in logs: log.close()
