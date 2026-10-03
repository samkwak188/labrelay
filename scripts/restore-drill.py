#!/usr/bin/env python3
"""Restore a catalog into a NEW local database; never overwrite the source catalog."""
import json,os,pathlib,subprocess,time,uuid,urllib.parse
root=pathlib.Path(__file__).resolve().parent.parent
out=root/'results/local'/('restore-'+time.strftime('%Y%m%dT%H%M%SZ',time.gmtime()));out.mkdir(parents=True)
name='labrelay_restore_'+uuid.uuid4().hex
env=os.environ.copy();url=urllib.parse.urlsplit(env['DATABASE_URL']);env['DATABASE_URL']=urllib.parse.urlunsplit(url._replace(path='/'+name))
started=time.monotonic();compose=['docker','compose','exec','-T','postgres']
with (out/'catalog.dump').open('wb') as f:subprocess.run(compose+['pg_dump','-U','labrelay','-d','labrelay','-Fc'],stdout=f,check=True)
subprocess.run(compose+['createdb','-U','labrelay',name],check=True)
with (out/'catalog.dump').open('rb') as f:subprocess.run(compose+['pg_restore','-U','labrelay','-d',name,'--exit-on-error'],stdin=f,check=True)
binary=str(root/'bin/labrelayd')
subprocess.run([binary,'maintenance'],env=env,check=True)
subprocess.run([binary,'validate-restore'],env=env,check=True)
with (out/'unknown-objects.jsonl').open('w') as f:subprocess.run([binary,'audit-storage'],env=env,stdout=f,check=True)
count=subprocess.check_output(compose+['psql','-U','labrelay','-d',name,'-Atc',"SELECT count(*) FROM ingestions WHERE state='READY'"],text=True).strip()
assert int(count)>0,'drill must include published data'
result={'kind':'local-new-database-restore','status':'pass','published_references_checked':int(count),'seconds':time.monotonic()-started,'restored_database':name,'fresh_cloud_host':False}
(out/'result.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result));print(out)
