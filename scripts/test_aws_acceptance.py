import importlib.util
import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('aws_preflight', ROOT/'scripts/aws-preflight.py')
preflight = importlib.util.module_from_spec(spec)
spec.loader.exec_module(preflight)
BASH = shutil.which('bash')
if not BASH and pathlib.Path('C:/Program Files/Git/bin/bash.exe').exists():
    BASH = 'C:/Program Files/Git/bin/bash.exe'


@unittest.skipUnless(BASH, 'Bash is required for test entry points')
class TestBackendSelection(unittest.TestCase):
    def run_backend(self, mode, extra=None):
        env = {key: value for key, value in os.environ.items()
               if not key.startswith(('AWS_', 'S3_', 'LABRELAY_', 'DATABASE_URL'))}
        env.update(DATABASE_URL='isolated-test-catalog', S3_BUCKET='dedicated-acceptance',
                   AWS_REGION='us-east-2', AWS_ACCESS_KEY_ID='test-caller-access',
                   AWS_SECRET_ACCESS_KEY='test-caller-secret', AWS_SESSION_TOKEN='test-caller-session')
        env.update(extra or {})
        command = '''set -euo pipefail
source scripts/test-env.sh "$1"
printf '%s\\n' "$LABRELAY_TEST_BACKEND" "${S3_ENDPOINT-}" "$S3_BUCKET" "$AWS_ACCESS_KEY_ID" "$AWS_SECRET_ACCESS_KEY" "${AWS_SESSION_TOKEN-}" "${AWS_IGNORE_CONFIGURED_ENDPOINT_URLS-}" "$DATABASE_URL"
'''
        return subprocess.run([BASH, '--noprofile', '--norc', '-c', command, 'backend-test', mode],
                              cwd=ROOT, env=env, capture_output=True, text=True, timeout=30)

    def test_aws_preserves_credentials_and_catalog(self):
        result = self.run_backend('aws')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), [
            'aws', '', 'dedicated-acceptance', 'test-caller-access',
            'test-caller-secret', 'test-caller-session', 'true', 'isolated-test-catalog',
        ])

    def test_aws_rejects_every_endpoint_override(self):
        for key in ('S3_ENDPOINT', 'AWS_ENDPOINT_URL', 'AWS_ENDPOINT_URL_S3'):
            with self.subTest(key=key):
                result = self.run_backend('aws', {key: 'http://127.0.0.1:18333'})
                self.assertEqual(result.returncode, 2)
                self.assertIn('real S3', result.stderr)
                self.assertEqual(result.stdout, '')

    def test_aws_requires_explicit_resources(self):
        for key in ('S3_BUCKET', 'DATABASE_URL', 'AWS_REGION'):
            with self.subTest(key=key):
                self.assertNotEqual(self.run_backend('aws', {key: ''}).returncode, 0)

    def test_local_selects_loopback_services(self):
        result = self.run_backend('local')
        self.assertEqual(result.returncode, 0, result.stderr)
        fields = result.stdout.splitlines()
        self.assertEqual(fields[:4], ['local', 'http://127.0.0.1:18333', 'labrelay-local', 'labrelay-local'])
        self.assertEqual(fields[5:7], ['', ''])

    def test_unknown_backend_is_rejected(self):
        self.assertEqual(self.run_backend('other').returncode, 2)


class TestBucketPreflight(unittest.TestCase):
    def responses(self, version='Enabled', access=None, algorithm='AES256'):
        return [
            {'Status': version},
            {'PublicAccessBlockConfiguration': access or {
                'BlockPublicAcls': True, 'IgnorePublicAcls': True,
                'BlockPublicPolicy': True, 'RestrictPublicBuckets': True,
            }},
            {'ServerSideEncryptionConfiguration': {'Rules': [
                {'ApplyServerSideEncryptionByDefault': {'SSEAlgorithm': algorithm}},
            ]}},
        ]

    def test_valid_bucket_uses_expected_owner_on_every_request(self):
        query = mock.Mock(side_effect=self.responses())
        checks = preflight.inspect_bucket('acceptance-bucket', '123456789012', query)
        self.assertEqual(checks['versioning'], 'Enabled')
        self.assertEqual(query.call_count, 3)
        for call in query.call_args_list:
            self.assertEqual(call.args[-4:], ('--bucket', 'acceptance-bucket', '--expected-bucket-owner', '123456789012'))

    def test_suspended_or_unversioned_bucket_is_rejected(self):
        for version in ('Suspended', None):
            with self.subTest(version=version):
                with self.assertRaisesRegex(ValueError, 'versioning'):
                    preflight.inspect_bucket('bucket', 'owner', mock.Mock(side_effect=self.responses(version=version)))

    def test_each_public_access_block_is_required(self):
        access = self.responses()[1]['PublicAccessBlockConfiguration']
        for key in access:
            with self.subTest(key=key):
                changed = dict(access, **{key: False})
                with self.assertRaisesRegex(ValueError, 'public-access'):
                    preflight.inspect_bucket('bucket', 'owner', mock.Mock(side_effect=self.responses(access=changed)))

    def test_unsupported_encryption_is_rejected(self):
        with self.assertRaisesRegex(ValueError, 'encryption'):
            preflight.inspect_bucket('bucket', 'owner', mock.Mock(side_effect=self.responses(algorithm='aws:kms')))

    def test_failed_preflight_retains_evidence_without_cli_output(self):
        error = subprocess.CalledProcessError(1, ['aws', 's3api', 'get-bucket-versioning'], stderr='sensitive-cli-output')
        with tempfile.TemporaryDirectory() as directory:
            env = {'LABRELAY_EVIDENCE_ROOT': directory, 'AWS_REGION': 'us-east-2', 'S3_BUCKET': 'test-bucket'}
            with mock.patch.dict(os.environ, env), mock.patch.object(preflight, 'aws_json', side_effect=error), mock.patch.object(preflight.subprocess, 'check_output', return_value='test-commit\n'):
                self.assertEqual(preflight.main(), 1)
            text = (pathlib.Path(directory)/'preflight.json').read_text()
            self.assertEqual(json.loads(text)['status'], 'fail')
            self.assertNotIn('sensitive-cli-output', text)


if __name__ == '__main__':
    unittest.main()
