#!/usr/bin/env python3
"""Download the pinned, public 33-artifact CATS fixture into a new directory.
Original video: Amada44, CC BY-SA 4.0; metadata preserves full attribution.
"""
import concurrent.futures,hashlib,json,pathlib,shutil,sys,urllib.parse,urllib.request
root=pathlib.Path(__file__).resolve().parent.parent;dest=pathlib.Path(sys.argv[1]);dest.mkdir(parents=True,exist_ok=False);fixture=root/'testdata/cats';meta=json.loads((fixture/'upstream.json').read_text());commit=meta['commit']
for a in meta['artifacts']:
    out=dest/a['path'];out.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(fixture/a['path'],out)
fm=json.loads((dest/'data/frames/chiangmai_intersection/manifest.json').read_text());scene=json.loads((dest/'data/scenes/chiangmai_intersection.json').read_text())
jobs=[]
for f in fm['frame_files']:
    jobs.append((f['path'],f'https://raw.githubusercontent.com/samkwak188/CATS-Lab----temporal-roadside-calibration/{commit}/'+f['path'],f['sha256'],f['size_bytes']))
filename='A_intersection_with_traffic_in_Chiang_Mai,_Thailand.ogv';md5=hashlib.md5(filename.encode(),usedforsecurity=False).hexdigest();video=next(i for i in fm['provenance']['inputs'] if i['path']==scene['storage_path'])
jobs.append((scene['storage_path'],f'https://upload.wikimedia.org/wikipedia/commons/{md5[0]}/{md5[:2]}/'+urllib.parse.quote(filename),video['sha256'],video['size_bytes']))
def download(job):
    path,url,expected,size=job;out=dest/path;out.parent.mkdir(parents=True,exist_ok=True);h=hashlib.sha256();n=0
    with urllib.request.urlopen(urllib.request.Request(url,headers={'User-Agent':'LabRelay/0.1 dataset-integrity-test'}),timeout=60) as r,out.open('xb') as f:
        while b:=r.read(1<<20):h.update(b);n+=len(b);f.write(b)
    if h.hexdigest()!=expected or n!=size:raise ValueError('upstream artifact mismatch: '+path)
    return path
with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
    for path in pool.map(download,jobs): print('verified',path,flush=True)
print(json.dumps({'artifacts':len(jobs)+3,'root':str(dest),'upstream_commit':commit}))
