#!/usr/bin/env python3
"""Pinned upstream metadata only; original footage is intentionally not fabricated."""
import pathlib,urllib.request,hashlib,json
commit='26c14d651e2acf06dec4c90a0ba5aae530a3508c'
root=pathlib.Path(__file__).resolve().parent.parent/'testdata/cats'
paths=['data/scenes/chiangmai_intersection.json','data/frames/chiangmai_intersection/manifest.json','data/frames/chiangmai_intersection/manifest.csv']
records=[]
for p in paths:
    url=f'https://raw.githubusercontent.com/samkwak188/CATS-Lab----temporal-roadside-calibration/{commit}/{p}'
    data=urllib.request.urlopen(url).read();dest=root/p;dest.parent.mkdir(parents=True,exist_ok=True);dest.write_bytes(data);records.append({'path':p,'url':url,'sha256':hashlib.sha256(data).hexdigest()})
(root/'upstream.json').write_text(json.dumps({'commit':commit,'artifacts':records},indent=2)+'\n')
print(json.dumps({'commit':commit,'downloaded_metadata':len(records)}))
