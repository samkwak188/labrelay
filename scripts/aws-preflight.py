#!/usr/bin/env python3
"""Read-only checks of the AWS acceptance bucket; never configure or create it."""
import json
import os
import pathlib
import subprocess
import sys
import time


def aws_json(*args):
    result = subprocess.run(
        ['aws', *args, '--output', 'json', '--no-cli-pager'],
        check=True, capture_output=True, text=True, timeout=60,
    )
    return json.loads(result.stdout)


def inspect_bucket(bucket, account, query=None):
    query = query or aws_json
    owner = ['--bucket', bucket, '--expected-bucket-owner', account]
    versioning = query('s3api', 'get-bucket-versioning', *owner)
    if versioning.get('Status') != 'Enabled':
        raise ValueError('acceptance bucket versioning must be Enabled')
    access = query('s3api', 'get-public-access-block', *owner)['PublicAccessBlockConfiguration']
    required = ('BlockPublicAcls', 'IgnorePublicAcls', 'BlockPublicPolicy', 'RestrictPublicBuckets')
    if any(access.get(key) is not True for key in required):
        raise ValueError('all four bucket public-access blocks must be enabled')
    encryption = query('s3api', 'get-bucket-encryption', *owner)
    rules = encryption.get('ServerSideEncryptionConfiguration', {}).get('Rules', [])
    if not rules or any(rule.get('ApplyServerSideEncryptionByDefault', {}).get('SSEAlgorithm') != 'AES256' for rule in rules):
        raise ValueError('acceptance bucket must use SSE-S3 (AES256) encryption')
    return {'versioning': 'Enabled', 'public_access_block': access, 'encryption': 'AES256'}


def main():
    out = pathlib.Path(os.environ['LABRELAY_EVIDENCE_ROOT']) / 'preflight.json'
    record = {'status': 'fail', 'storage_backend': 'aws', 'region': os.environ['AWS_REGION'],
              'bucket': os.environ['S3_BUCKET'], 'checked_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
              'commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip(),
              'workflow_run': os.environ.get('GITHUB_RUN_ID')}
    try:
        identity = aws_json('sts', 'get-caller-identity')
        record.update(aws_account_id=identity['Account'], caller_arn=identity['Arn'])
        record['checks'] = inspect_bucket(record['bucket'], identity['Account'])
        record['status'] = 'pass'
    except (OSError, subprocess.SubprocessError, ValueError, KeyError) as error:
        # Do not put AWS CLI output, credential configuration, or the environment in evidence.
        record['error'] = str(error)
        print('AWS preflight failed; check bucket ownership, versioning, privacy, encryption and role permissions.', file=sys.stderr)
        return 1
    finally:
        out.write_text(json.dumps(record, indent=2)+'\n')
    print('AWS acceptance bucket preflight: PASS')
    return 0


if __name__ == '__main__':
    sys.exit(main())
