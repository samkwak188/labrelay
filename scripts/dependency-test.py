#!/usr/bin/env python3
"""Actual local dependency outages; starts only this project's Compose services."""
import json,os,pathlib,subprocess,tempfile,time,urllib.request,uuid
root=pathlib.Path(__file__).resolve().parent.parent;out=root/'results/local'/('dependencies-'+time.strftime('%Y%m%dT%H%M%SZ',time.gmtime()));out.mkdir(parents=True);env=os.environ.copy();project='dependency-'+uuid.uuid4().hex;token=subprocess.check_output([str(root/'bin/labrelayd'),'token',project,'write'],env=env,text=True).strip();env.update(LABRELAY_TOKEN=token,LABRELAY_PROJECT=project,LABRELAY_LISTEN='127.0.0.1:18085',LABRELAY_SERVER='http://127.0.0.1:18085');log=(out/'process.log').open('w');daemon=None;results=[]
def start():
    global daemon
    daemon=subprocess.Popen([str(root/'bin/labrelayd'),'serve'],env=env,stdout=log,stderr=log)
    for _ in range(100):
        try:urllib.request.urlopen(env['LABRELAY_SERVER']+'/health/ready',timeout=1).close();return
        except Exception:time.sleep(.1)
    raise TimeoutError('readiness')
def service(name,action):subprocess.run(['docker','compose',action,name],cwd=root,stdout=log,stderr=log,check=True)
try:
    for dependency in ('seaweed','postgres'):
        with tempfile.TemporaryDirectory(prefix='labrelay-dependency-') as d:
            env['LABRELAY_SPOOL']=d+'/spool';src=pathlib.Path(d)/'source';src.mkdir();(src/'data').write_bytes(b'outage recovery fixture');cli=str(root/'bin/labrelay');subprocess.run([cli,'prepare','--root',str(src),'--name',dependency],env=env,stdout=log,stderr=log,check=True);start();service(dependency,'stop');collector=subprocess.Popen([cli,'sync'],env=env,stdout=log,stderr=log);time.sleep(7);assert collector.poll() is None,'collector falsely finished while dependency down';recover=time.monotonic();service(dependency,'start')
            if dependency=='postgres':
                daemon.wait(timeout=30)
                assert daemon.returncode!=0,'lost-lock daemon must exit for supervisor restart'
                for _ in range(60):
                    p=subprocess.run(['docker','compose','exec','-T','postgres','pg_isready','-U','labrelay'],cwd=root,stdout=log,stderr=log)
                    if p.returncode==0:break
                    time.sleep(.5)
                start()
            collector.wait(timeout=120);assert collector.returncode==0;status=json.loads(subprocess.check_output([cli,'status'],env=env,text=True));assert status[0]['state']=='PUBLISHED';revision=status[0]['receipt']['revision_id'];subprocess.run([cli,'fetch',revision,'--dest',d+'/download'],env=env,check=True,stdout=log,stderr=log);results.append({'dependency':dependency,'status':'pass','recovery_seconds':time.monotonic()-recover});daemon.terminate();daemon.wait(timeout=50);daemon=None;(out/'results.json').write_text(json.dumps(results,indent=2)+'\n');print(dependency+': PASS',flush=True)
finally:
    if daemon and daemon.poll() is None:daemon.terminate();daemon.wait(timeout=50)
    for dependency in ('seaweed','postgres'):service(dependency,'start')
    log.close();print(out)
