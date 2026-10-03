#!/usr/bin/env python3
"""Install the pinned validation tool into a Linux temporary tools directory."""
import hashlib, pathlib, urllib.request, zipfile, io
version='1.16.5'
base=f'https://releases.hashicorp.com/terraform/{version}/'
name=f'terraform_{version}_linux_amd64.zip'
checks=urllib.request.urlopen(base+f'terraform_{version}_SHA256SUMS').read().decode()
expected=next(line.split()[0] for line in checks.splitlines() if line.endswith(' '+name))
data=urllib.request.urlopen(base+name).read()
assert hashlib.sha256(data).hexdigest()==expected
dest=pathlib.Path('/tmp/labrelay-tools');dest.mkdir(exist_ok=True)
with zipfile.ZipFile(io.BytesIO(data)) as z: (dest/'terraform').write_bytes(z.read('terraform'))
(dest/'terraform').chmod(0o755)
print('Installed Terraform',version,'archive sha256',expected)
