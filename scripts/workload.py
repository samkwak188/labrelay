#!/usr/bin/env python3
"""Deterministic pseudorandom fixtures. Refuses existing destination directories."""
import argparse,hashlib,pathlib
p=argparse.ArgumentParser();p.add_argument('kind',choices=['large','many']);p.add_argument('destination',type=pathlib.Path);p.add_argument('--bytes',type=int,default=256<<20);p.add_argument('--files',type=int,default=5000);a=p.parse_args();a.destination.mkdir(parents=True,exist_ok=False)
if a.kind=='large':
    with (a.destination/'large.bin').open('wb') as f:
        for offset in range(0,a.bytes,1<<20):f.write(hashlib.shake_256(f'labrelay-v1-{offset}'.encode()).digest(min(1<<20,a.bytes-offset)))
else:
    for i in range(a.files):
        path=a.destination/f'{i//100:02d}'/f'{i:05d}.bin';path.parent.mkdir(exist_ok=True)
        path.write_bytes(hashlib.shake_256(f'labrelay-v1-file-{i}'.encode()).digest(4096))
print(a.destination)
