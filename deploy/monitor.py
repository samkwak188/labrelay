#!/usr/bin/env python3
import json, os, pathlib, shutil, subprocess, time, urllib.request
values={};ready=0
try:
    with urllib.request.urlopen('http://127.0.0.1:8080/health/ready',timeout=5) as r: ready=int(r.status==200)
    with urllib.request.urlopen('http://127.0.0.1:8080/metrics',timeout=5) as r:
        for line in r.read().decode().splitlines():
            if not line.startswith('#') and '{' not in line:
                p=line.split()
                if len(p)==2:values[p[0]]=float(p[1])
except Exception: pass
state=pathlib.Path('/srv/labrelay/monitor-state.json');now=time.time();old={}
if state.exists():old=json.loads(state.read_text())
count=values.get('labrelay_verified_files_total',0)
last=old.get('last_progress',now)
if count!=old.get('count') or values.get('labrelay_verification_pending_files',0)==0:last=now
state.write_text(json.dumps({'count':count,'last_progress':last}))
disk=shutil.disk_usage('/srv/labrelay');dims=[{'Name':'InstanceId','Value':os.environ['INSTANCE_ID']}]
data=[{'MetricName':name,'Value':value,'Dimensions':dims} for name,value in [('Heartbeat',ready),('DiskUsedPercent',100*(disk.total-disk.free)/disk.total),('VerificationStalled',int(now-last>300))]]
subprocess.run(['aws','cloudwatch','put-metric-data','--namespace','LabRelay','--metric-data',json.dumps(data)],check=True)
