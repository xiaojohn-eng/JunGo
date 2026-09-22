#!/usr/bin/env python3
"""Loopback-only HTTP CONNECT proxy for emulator routing acceptance."""
import json,pathlib,select,socket,socketserver,time
ROOT=pathlib.Path(__file__).resolve().parents[1]
LOG=ROOT/'.state/android-qa/proxy-connect.jsonl'
LOG.parent.mkdir(parents=True,exist_ok=True)
class Handler(socketserver.BaseRequestHandler):
    def handle(self):
        incoming=self.request;incoming.settimeout(10);data=b''
        while b'\r\n\r\n' not in data and len(data)<16384:
            chunk=incoming.recv(1024)
            if not chunk:return
            data+=chunk
        first=data.split(b'\r\n',1)[0].decode('ascii','strict').split()
        if len(first)!=3 or first[0]!='CONNECT':incoming.sendall(b'HTTP/1.1 405 Method Not Allowed\r\nContent-Length:0\r\n\r\n');return
        host,port=first[1].rsplit(':',1)
        if host not in {'example.com','www.example.com','10.0.2.2'} or int(port) not in {443,9081}:
            incoming.sendall(b'HTTP/1.1 403 Forbidden\r\nContent-Length:0\r\n\r\n');return
        if host=='10.0.2.2':host='127.0.0.1'
        try:outgoing=socket.create_connection((host,int(port)),20)
        except OSError:incoming.sendall(b'HTTP/1.1 502 Bad Gateway\r\nContent-Length:0\r\n\r\n');return
        with outgoing:
            with LOG.open('a') as log:log.write(json.dumps({'time':time.time(),'connect':first[1]})+'\n')
            incoming.sendall(b'HTTP/1.1 200 Connection Established\r\n\r\n')
            incoming.settimeout(None);outgoing.settimeout(None)
            while True:
                ready,_,_=select.select([incoming,outgoing],[],[],60)
                if not ready:return
                for src in ready:
                    block=src.recv(65536)
                    if not block:return
                    (outgoing if src is incoming else incoming).sendall(block)
class Server(socketserver.ThreadingTCPServer):
    allow_reuse_address=True
    daemon_threads=True
with Server(('127.0.0.1',9888),Handler) as server:
    print('QA CONNECT proxy listening on loopback:9888',flush=True)
    try:server.serve_forever()
    except KeyboardInterrupt:pass
