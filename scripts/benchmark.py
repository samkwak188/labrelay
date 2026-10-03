#!/usr/bin/env python3
"""Paired measurements with raw logs; local exploratory evidence, not cloud claims."""
import argparse,hashlib,json,os,pathlib,platform,resource,subprocess,tempfile,threading,time,urllib.request,uuid
p=argparse.ArgumentParser();p.add_argument('--repetitions',type=int,default=10);p.add_argument('--bytes',type=int,default=128<<20);a=p.parse_args()
root=pathlib.Path(__file__).resolve().parent.parent;out=root/'results/local'/('benchmark-'+time.strftime('%Y%m%dT%H%M%SZ',time.gmtime()));out.mkdir(parents=True);env=os.environ.copy();project='bench-'+uuid.uuid4().hex;token=subprocess.check_output([str(root/'bin/labrelayd'),'token',project,'write'],env=env,text=True).strip();env.update(LABRELAY_TOKEN=token,LABRELAY_PROJECT=project,LABRELAY_LISTEN='127.0.0.1:18083',LABRELAY_SERVER='http://127.0.0.1:18083');log=(out/'process.log').open('w');records=[]
def metrics():
    with urllib.request.urlopen(env['LABRELAY_SERVER']+'/metrics') as r:return r.read().decode()
def metric(text,name):
    return next((float(x.split()[1]) for x in text.splitlines() if x.startswith(name+' ')),0)
def run(cmd,extra=None):
    e=env.copy();e.update(extra or {});before=resource.getrusage(resource.RUSAGE_CHILDREN);start=time.monotonic();proc=subprocess.Popen(cmd,env=e,stdout=subprocess.PIPE,stderr=log,text=True);peak=0
    while proc.poll() is None:
        try:
            for line in pathlib.Path(f'/proc/{proc.pid}/status').read_text().splitlines():
                if line.startswith('VmHWM:'):peak=max(peak,int(line.split()[1])*1024)
        except FileNotFoundError:pass
        time.sleep(.01)
    stdout=proc.communicate()[0];after=resource.getrusage(resource.RUSAGE_CHILDREN)
    if proc.returncode:raise RuntimeError(f'{cmd[0]} failed: {stdout}')
    return {'wall_seconds':time.monotonic()-start,'peak_rss_bytes_sampled':peak,'cpu_seconds':after.ru_utime+after.ru_stime-before.ru_utime-before.ru_stime,'output':json.loads(stdout)}
def ready(port):
    for _ in range(100):
        try:urllib.request.urlopen(f'http://127.0.0.1:{port}/health/live',timeout=1).close();return
        except Exception:time.sleep(.1)
    raise TimeoutError('server startup')
servers=[]
try:
    for binary,port in [('labrelayd',18083),('tus-baseline',18082)]:
        cmd='serve' if binary=='labrelayd' else 'server';proc=subprocess.Popen([str(root/'bin'/binary),cmd],env=env,stdout=log,stderr=log);servers.append(proc);ready(port)
    with tempfile.TemporaryDirectory(prefix='labrelay-bench-') as d:
        source=pathlib.Path(d)/'source';subprocess.run(['python3',str(root/'scripts/workload.py'),'large',str(source),'--bytes',str(a.bytes)],check=True,stdout=log)
        for i in range(a.repetitions+1):
            entry={'iteration':i,'warmup':i==0,'bytes':a.bytes}
            try:
                # Alternate order to reduce systematic cache/order bias.
                for kind in (['baseline','labrelay'] if i%2==0 else ['labrelay','baseline']):
                    if kind=='baseline':entry[kind]=run([str(root/'bin/tus-baseline'),'upload',str(source/'large.bin')])
                    else:
                        spool=str(pathlib.Path(d)/f'spool-{i}');extra={'LABRELAY_SPOOL':spool}
                        entry['prepare']=run([str(root/'bin/labrelay'),'prepare','--root',str(source),'--name',f'large-{i}'],extra)
                        before=metrics();entry[kind]=run([str(root/'bin/labrelay'),'sync'],extra);after=metrics();(out/f'metrics-{i}.txt').write_text(after)
                        entry['verification_seconds']=metric(after,'labrelay_verification_seconds_sum')-metric(before,'labrelay_verification_seconds_sum')
                        entry['spool_bytes']=sum(f.stat().st_size for f in pathlib.Path(spool).rglob('*') if f.is_file())
                baseline=entry['baseline']['output']['upload_window_seconds'];lab=entry['labrelay']['output']['upload_window_seconds'];entry['throughput_ratio']=baseline/lab;entry['status']='pass'
            except Exception as e:entry['status']='fail';entry['error']=repr(e);raise
            finally:records.append(entry);(out/'raw.json').write_text(json.dumps(records,indent=2)+'\n')
            print(f'iteration {i}: ratio={entry["throughput_ratio"]:.3f}',flush=True)
        versions={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in (root/'bin').glob('*') if p.is_file()}
        (out/'environment.json').write_text(json.dumps({'platform':platform.platform(),'cpus':os.cpu_count(),'repetitions':a.repetitions,'bytes':a.bytes,'binary_sha256':versions,'limitations':['same-host local storage; not AWS','RSS sampled at 10ms','object request counts available as raw tusd metrics; not complete S3 accounting','no network shaping; 128 MiB default is exploratory']},indent=2)+'\n')
finally:
    for proc in servers:
        proc.terminate()
        try:proc.wait(timeout=50)
        except subprocess.TimeoutExpired:proc.kill();proc.wait()
    log.close();print('Raw evidence:',out)
