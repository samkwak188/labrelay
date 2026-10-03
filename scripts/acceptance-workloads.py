#!/usr/bin/env python3
"""Separate CLI/server processes: full CATS and 5,000-file preparation through fetch."""
import json,os,pathlib,subprocess,sys,tempfile,time,urllib.request,uuid
root=pathlib.Path(__file__).resolve().parent.parent;out=root/'results/local'/('workloads-'+time.strftime('%Y%m%dT%H%M%SZ',time.gmtime()));out.mkdir(parents=True);env=os.environ.copy();project='workload-'+uuid.uuid4().hex;token=subprocess.check_output([str(root/'bin/labrelayd'),'token',project,'write'],env=env,text=True).strip();env.update(LABRELAY_TOKEN=token,LABRELAY_PROJECT=project,LABRELAY_LISTEN='127.0.0.1:18084',LABRELAY_SERVER='http://127.0.0.1:18084');log=(out/'process.log').open('w');server=subprocess.Popen([str(root/'bin/labrelayd'),'serve'],env=env,stdout=log,stderr=log);results=[]
def run(args,env):
    start=time.monotonic();p=subprocess.run([str(root/'bin/labrelay')]+args,env=env,stdout=subprocess.PIPE,stderr=log,text=True,timeout=1200)
    if p.returncode:raise RuntimeError(str(args)+': '+p.stdout+'; inspect process.log')
    return {'seconds':time.monotonic()-start,'result':json.loads(p.stdout)}
try:
    for _ in range(100):
        try:urllib.request.urlopen(env['LABRELAY_SERVER']+'/health/ready').close();break
        except Exception:time.sleep(.1)
    with tempfile.TemporaryDirectory(prefix='labrelay-workloads-') as d:
        many=pathlib.Path(d)/'many';subprocess.run(['python3',str(root/'scripts/workload.py'),'many',str(many)],check=True,stdout=log)
        jobs=[('many-5000',['prepare','--root',str(many),'--name','many-5000'],5000)]
        if len(sys.argv)>1:jobs.insert(0,('cats',['import','cats','--root',sys.argv[1],'--scene','data/scenes/chiangmai_intersection.json','--frames','data/frames/chiangmai_intersection/manifest.json'],33))
        for name,args,count in jobs:
            e=env.copy();e['LABRELAY_SPOOL']=d+'/'+name+'-spool';r={'workload':name};r['prepare']=run(args,e);assert r['prepare']['result']['files']==count;print(name+': prepared',flush=True);r['sync']=run(['sync'],e);print(name+': published',flush=True);status=run(['status'],e)['result'];receipt=status[0]['receipt'];assert len(receipt['files'])==count;r['revision']=receipt['revision_id'];r['fetch']=run(['fetch',r['revision'],'--dest',d+'/'+name+'-download'],e);r['status']='pass';results.append(r);(out/'results.json').write_text(json.dumps(results,indent=2)+'\n');print(name+': fetched and verified',flush=True)
        with urllib.request.urlopen(env['LABRELAY_SERVER']+'/metrics') as response:(out/'metrics.txt').write_bytes(response.read())
finally:
    server.terminate();server.wait(timeout=50);log.close();print(out)
