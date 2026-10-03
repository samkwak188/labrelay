#!/usr/bin/env python3
"""Actual daemon termination and response-loss tests. Uses isolated project IDs.
Requires an explicit test backend and built binaries. Raw logs/results are retained.
"""
import base64, hashlib, http.client, http.server, json, os, pathlib, signal, socket, subprocess, tempfile, threading, time, urllib.error, urllib.request, uuid

ROOT = pathlib.Path(__file__).resolve().parent.parent
OUT = pathlib.Path(os.environ.get('LABRELAY_EVIDENCE_ROOT', ROOT / 'results' / 'local')) / ('faults-' + time.strftime('%Y%m%dT%H%M%SZ', time.gmtime()))
OUT.mkdir(parents=True, exist_ok=False)
ENV = os.environ.copy()
(OUT/'environment.json').write_text(json.dumps({'storage_backend': ENV.get('LABRELAY_TEST_BACKEND', 'unspecified'), 'region': ENV.get('AWS_REGION'), 'bucket': ENV.get('S3_BUCKET'), 'commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip(), 'workflow_run': ENV.get('GITHUB_RUN_ID')}, indent=2)+'\n')
PROJECT = 'fault-' + uuid.uuid4().hex
TOKEN = subprocess.check_output([str(ROOT/'bin/labrelayd'), 'token', PROJECT, 'write'], env=ENV, text=True).strip()
ENV.update(LABRELAY_TOKEN=TOKEN, LABRELAY_PROJECT=PROJECT)
PORT = 18081
ENV['LABRELAY_LISTEN'] = f'127.0.0.1:{PORT}'
BASE = f'http://127.0.0.1:{PORT}'
proc = None
log = (OUT/'server.log').open('w')
records = []

def call(method, path, body=None, headers=None, base=BASE):
    h={'Authorization': 'Bearer '+TOKEN}
    if body is not None: h['Content-Type']='application/json'
    h.update(headers or {})
    r=urllib.request.Request(base+path, data=body, headers=h, method=method)
    with urllib.request.urlopen(r, timeout=30) as resp:
        data=resp.read()
        return resp.status, dict(resp.headers), json.loads(data) if data else None

def start(point=''):
    global proc
    env=ENV.copy();env['LABRELAY_FAULT']=point
    proc=subprocess.Popen([str(ROOT/'bin/labrelayd'),'serve'],env=env,stdout=log,stderr=log)
    for _ in range(100):
        if proc.poll() is not None: raise RuntimeError('server startup failed; see server.log')
        try:
            call('GET','/health/ready');return
        except Exception: time.sleep(.1)
    raise TimeoutError('server readiness')

def stop(kill=False):
    global proc
    if proc and proc.poll() is None:
        proc.send_signal(signal.SIGKILL if kill else signal.SIGTERM)
        proc.wait(timeout=50)
    proc=None

def crashed():
    proc.wait(timeout=30)
    assert proc.returncode==86, proc.returncode

def register(name, data):
    m={'schema_version':1,'name':name,'source_type':'generic','files':[{'path':'data','role':'data','size_bytes':len(data),'sha256':hashlib.sha256(data).hexdigest()}],'provenance':[]}
    raw=json.dumps(m,separators=(',',':')).encode()+b'\n'
    path=f'/v1/projects/{PROJECT}/datasets/{name}/ingestions'
    return raw,path,call('POST',path,raw)[2]

def attempt(st):
    return call('POST',f"/v1/ingestions/{st['id']}/files/{st['files'][0]['id']}/attempts",b'')[2]['url']

def patch(url,data,offset=0,base=BASE):
    return call('PATCH',url,data,{'Content-Type':'application/offset+octet-stream','Tus-Resumable':'1.0.0','Upload-Offset':str(offset)},base)

def head(url):
    return int(call('HEAD',url,headers={'Tus-Resumable':'1.0.0'})[1]['Upload-Offset'])

def finish(st):
    path=f"/v1/ingestions/{st['id']}"
    call('POST',path+'/finalize',b'')
    for _ in range(200):
        result=call('GET',path)[2]
        if result['state']=='READY': return result
        assert result['state'] not in ('FAILED','EXPIRED'),result
        time.sleep(.1)
    raise TimeoutError('publication')

def record(name,fn):
    began=time.monotonic()
    try: detail=fn();records.append({'scenario':name,'status':'pass','seconds':time.monotonic()-began,'detail':detail})
    except Exception as e:
        records.append({'scenario':name,'status':'fail','seconds':time.monotonic()-began,'error':repr(e)});raise
    finally:
        stop();(OUT/'results.json').write_text(json.dumps(records,indent=2)+'\n')
    print(name+': PASS',flush=True)

def creation(point):
    data=b'creation-response-recovery';start(point);raw,path,st=register(point,data)
    try: attempt(st)
    except (http.client.HTTPException,OSError,urllib.error.URLError): pass
    crashed();start();again=call('POST',path,raw)[2];assert again['id']==st['id'];url=attempt(again);assert url==attempt(again)
    patch(url,data);result=finish(again);assert result['revision_id'];return {'revision':result['revision_id'],'upload_id':url.split('/')[-1]}

def publication(point):
    data=b'publication-boundary';start(point);raw,path,st=register(point,data);url=attempt(st)
    try:
        patch(url,data)
        if point!='object_verified': call('POST',f"/v1/ingestions/{st['id']}/finalize",b'')
    except (http.client.HTTPException,OSError,urllib.error.URLError): pass
    crashed();start();again=call('POST',path,raw)[2];result=finish(again);repeat=finish(again);assert result['revision_id']==repeat['revision_id'];return {'revision':result['revision_id']}

def resume_after_kill():
    data=(hashlib.sha256(b'incompressible-seed').digest()*((8<<20)//32))+b'end';start();raw,path,st=register('sigkill',data);url=attempt(st);patch(url,data[:8<<20]);offset=head(url);assert offset==8<<20;stop(True);start();assert head(url)==offset;patch(url,data[offset:],offset);result=finish(st);return {'acknowledged_offset':offset,'remaining_bytes':len(data)-offset,'revision':result['revision_id']}

class LossProxy(http.server.BaseHTTPRequestHandler):
    drops={'create':1,'patch':1,'partial':1}
    lock=threading.Lock()
    def log_message(self,*a): pass
    def forward(self):
        body=self.rfile.read(int(self.headers.get('Content-Length','0')))
        c=http.client.HTTPConnection('127.0.0.1',PORT,timeout=45)
        with self.lock:
            partial=self.command=='PATCH' and self.drops['partial']>0
            if partial:self.drops['partial']-=1
        if partial:
            c.request(self.command,self.path,body[:len(body)//2],dict(self.headers))
            c.sock.shutdown(socket.SHUT_RDWR);c.close()
            self.close_connection=True;self.connection.shutdown(socket.SHUT_RDWR);self.connection.close();return
        c.request(self.command,self.path,body,dict(self.headers));r=c.getresponse();data=r.read();headers=r.getheaders();status=r.status;c.close()
        kind='create' if self.command=='POST' and self.path.endswith('/attempts') else 'patch' if self.command=='PATCH' else ''
        with self.lock:
            drop=status<300 and self.drops.get(kind,0)>0
            if drop:self.drops[kind]-=1
        if drop:
            self.close_connection=True
            self.connection.shutdown(socket.SHUT_RDWR);self.connection.close();return
        self.send_response(status)
        for k,v in headers:
            if k.lower() not in ('connection','transfer-encoding','server','date'):self.send_header(k,v)
        self.end_headers()
        if self.command!='HEAD':self.wfile.write(data)
    do_GET=do_POST=do_HEAD=do_PATCH=forward

def lost_responses():
    start();proxy=http.server.ThreadingHTTPServer(('127.0.0.1',0),LossProxy);thread=threading.Thread(target=proxy.serve_forever,daemon=True);thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix='labrelay-fault-') as d:
            src=pathlib.Path(d)/'source';src.mkdir();data=hashlib.shake_256(b'labrelay-network-fixture').digest((16<<20)+17);(src/'data').write_bytes(data)
            env=ENV.copy();env.update(LABRELAY_SERVER=f'http://127.0.0.1:{proxy.server_port}',LABRELAY_SPOOL=d+'/spool')
            cli=str(ROOT/'bin/labrelay');subprocess.run([cli,'prepare','--root',str(src),'--name','loss'],env=env,check=True,stdout=log,stderr=log)
            began=time.monotonic();out=subprocess.check_output([cli,'sync'],env=env,stderr=log,text=True,timeout=180);elapsed=time.monotonic()-began
            bs=json.loads(subprocess.check_output([cli,'status'],env=env,text=True));receipt=bs[0]['receipt'];assert receipt['state']=='READY';assert LossProxy.drops=={'create':0,'patch':0,'partial':0}
            dest=d+'/download';subprocess.run([cli,'fetch',receipt['revision_id'],'--dest',dest],env=env,check=True,stdout=log,stderr=log);assert (pathlib.Path(dest)/'data').read_bytes()==data
            stats=json.loads(out);assert stats['bytes_sent']<=len(data)+(8<<20),stats
            return {'seconds':elapsed,'source_bytes':len(data),'sync':stats,'dropped_creation_responses':1,'dropped_patch_responses':1,'interrupted_patch_bodies':1}
    finally:proxy.shutdown();proxy.server_close();thread.join()

try:
    for p in ('attempt_reserved','attempt_storage_created','attempt_committed'):record(p,lambda p=p:creation(p))
    for p in ('object_verified','before_publication_commit','after_publication_commit'):record(p,lambda p=p:publication(p))
    record('SIGKILL_after_acknowledged_PATCH',resume_after_kill)
    record('lost_creation_and_PATCH_responses',lost_responses)
finally:
    stop();log.close();print('Raw evidence:',OUT)
