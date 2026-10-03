#!/usr/bin/env python3
"""Restore into a NEW Compose database and test failures without modifying S3."""
import json
import os
import pathlib
import subprocess
import time
import urllib.parse
import uuid

root = pathlib.Path(__file__).resolve().parent.parent
out = pathlib.Path(os.environ.get('LABRELAY_EVIDENCE_ROOT', root/'results/local')) / ('restore-'+time.strftime('%Y%m%dT%H%M%SZ', time.gmtime()))
out.mkdir(parents=True)
name = 'labrelay_restore_'+uuid.uuid4().hex
env = os.environ.copy()
url = urllib.parse.urlsplit(env['DATABASE_URL'])
if (url.hostname, url.port, url.username, url.path) != ('127.0.0.1', 15432, 'labrelay', '/labrelay'):
    raise ValueError('restore drill requires the isolated local Compose labrelay catalog')
env['DATABASE_URL'] = urllib.parse.urlunsplit(url._replace(path='/'+name))
compose = ['docker', 'compose', 'exec', '-T', 'postgres']
binary = str(root/'bin/labrelayd')
started = time.monotonic()
result = {'kind': 'local-new-database-restore', 'status': 'fail',
          'storage_backend': env.get('LABRELAY_TEST_BACKEND', 'unspecified'),
          'commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip(),
          'workflow_run': env.get('GITHUB_RUN_ID'),
          'restored_database': name, 'fresh_cloud_host': False, 'failure_checks': []}


def sql(statement):
    return subprocess.check_output(
        compose+['psql', '-X', '-v', 'ON_ERROR_STOP=1', '-U', 'labrelay', '-d', name, '-Atc', statement],
        text=True, timeout=60,
    ).strip()


def admin(command, log_name, expect_success=True):
    with (out/log_name).open('w') as log:
        completed = subprocess.run([binary, command], env=env, stdout=log, stderr=log, timeout=180)
    if (completed.returncode == 0) != expect_success:
        raise RuntimeError(f'{command}: unexpected exit {completed.returncode}; see {log_name}')


try:
    with (out/'catalog.dump').open('wb') as dump:
        subprocess.run(compose+['pg_dump', '-U', 'labrelay', '-d', 'labrelay', '-Fc'], stdout=dump, check=True, timeout=120)
    subprocess.run(compose+['createdb', '-U', 'labrelay', name], check=True, timeout=60)
    with (out/'catalog.dump').open('rb') as dump:
        subprocess.run(compose+['pg_restore', '-U', 'labrelay', '-d', name, '--exit-on-error'], stdin=dump, check=True, timeout=120)

    revisions = int(sql("SELECT count(*) FROM ingestions WHERE state='READY'"))
    files = int(sql("SELECT count(*) FROM files f JOIN ingestions i ON i.id=f.ingestion_id WHERE i.state='READY'"))
    if revisions == 0 or files == 0:
        raise RuntimeError('drill must include published data')
    result.update(published_revisions_checked=revisions, published_files_checked=files)
    admin('maintenance', 'maintenance.log')
    admin('validate-restore', 'initial-validation.log')
    if sql("SELECT value FROM settings WHERE key='maintenance'") != 'false':
        raise RuntimeError('successful validation did not reopen the restored catalog')

    original = json.loads(sql("SELECT json_build_object('id',f.id,'version',f.version_id,'hash',f.sha256,'size',f.size_bytes) FROM files f JOIN ingestions i ON i.id=f.ingestion_id WHERE i.state='READY' LIMIT 1"))

    def quote(value):
        return "'"+str(value).replace("'", "''")+"'"

    file_id = quote(original['id'])
    reset = f"UPDATE files SET version_id={quote(original['version'])},sha256={quote(original['hash'])},size_bytes={int(original['size'])} WHERE id={file_id}"
    failures = [
        ('missing-version', 'version_id='+quote('missing-'+uuid.uuid4().hex)),
        ('empty-version', "version_id=''"),
        ('null-version', "version_id='null'"),
        ('corrupt-hash', "sha256=repeat('0',64)"),
        ('wrong-size', f"size_bytes={int(original['size'])+1}"),
    ]
    for scenario, change in failures:
        admin('maintenance', scenario+'-maintenance.log')
        sql(f'UPDATE files SET {change} WHERE id={file_id}')
        try:
            admin('validate-restore', scenario+'.log', expect_success=False)
            if sql("SELECT value FROM settings WHERE key='maintenance'") != 'true':
                raise RuntimeError(scenario+': failed validation reopened the catalog')
            result['failure_checks'].append({'scenario': scenario, 'status': 'pass', 'maintenance_retained': True})
        finally:
            # Only the new drill database is repaired. Source catalog and S3 are untouched.
            sql(reset)

    admin('validate-restore', 'repaired-validation.log')
    if sql("SELECT value FROM settings WHERE key='maintenance'") != 'false':
        raise RuntimeError('repaired validation did not reopen the restored catalog')
    with (out/'unknown-objects.jsonl').open('w') as report:
        subprocess.run([binary, 'audit-storage'], env=env, stdout=report, check=True, timeout=180)
    result['status'] = 'pass'
except Exception as error:
    result['error'] = str(error)
    raise
finally:
    result['seconds'] = time.monotonic()-started
    (out/'result.json').write_text(json.dumps(result, indent=2)+'\n')
    print(json.dumps(result))
    print(out)
