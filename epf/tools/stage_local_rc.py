#!/usr/bin/env python3
"""Stage the specifically evidenced Graph candidate for local inspection only."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import sys

os.umask(0o077)
ROOT = Path(__file__).resolve().parents[2]
PROOF = Path('/private/tmp/epf-graph-20261109')
PACKETS = {
    'epf-pack.json': 'cf567c5509b7028f55f0d7713ebb3c77f4baefbecbe31a4c50c7bc0ecbb0b91f',
    'builds-v6.json': '53cbd51d8b769ca92715ae452bef64ca61d29c1c4d615d4186afa0a57f7c2306',
    'source-final-v6.json': 'ad34419ced20b3a4fd7bc63a4141b8c73cc0e0fa3c8987d438f0ba6c3bcf5946',
    'verification-v4.json': '1d110778d7bb118a139b0fff22bd05c6eaf943825f42d7764a198b25bf698259',
}
MEMBERS = {
    'MCPConnector.xml': ('68a0e6e1fad149f1528e6495d1f320d69e8b90796eaa3aafa9396bc0617d7fbb', '471caca55b8ca04146faa798750a7ef35fe4efa9b3a6534a3babec44d1356b28'),
    'MCPConnector/Forms/Main.xml': ('5f72ab41ab9a93751da5f4795242ea933e132aad8ce5816036fa26daee9b5e81', 'c4f1366ffe486ab79542ed4172643536a85b9d07ce72acc81783d797006da30c'),
    'MCPConnector/Forms/Main/Ext/Form.xml': ('777d65a4ace09f9318253ad7fbc1860f8fd43ca7b12dcacea29c27ac6faf315c', 'e9d2ce0022ad940de1314b40c567c2ad7ab1db4b978e419eb44cb237eb82799f'),
    'MCPConnector/Forms/Main/Ext/Form/Module.bsl': ('43be093835271b7895799486c0f02e6f90656857294b37eebce76b154ea39df6', '01442678eecebd0b04a7bc4bb852480e351fde62d9a86eb0af6ee06dce7d0c7d'),
}

def digest(data):
    return hashlib.sha256(data).hexdigest()

def checked(path, expected, size=None):
    data = path.read_bytes()
    if digest(data) != expected or (size is not None and len(data) != size):
        raise ValueError(f'input mismatch: {path.name}')
    return data

def stage(output, source, proof):
    packets = {name: checked(proof / 'public' / name, sha) for name, sha in PACKETS.items()}
    pack = json.loads(packets['epf-pack.json'])
    build = json.loads(packets['builds-v6.json'])
    binding = json.loads(packets['source-final-v6.json'])
    if (pack['status'] != 'PASS_PACK_READBACK' or not pack['bsl_equal'] or
        [c['rc'] for c in pack['commands']] != [0, 0] or
        pack['source'] != '3d30f0b5c7a3e29291683d67dfc34e6a1edf7026' or
        binding['C']['head'] != pack['source'] or not binding['C']['clean'] or
        build['tree'] != 'a50f3a2a1f06a2c75770efe5de1cc7c61f89a3a1' or
        any(c['rc'] != 0 for c in build['commands']) or not build['anchor_removed']):
        raise ValueError('proof packet binding mismatch')
    files = {str(p.relative_to(source)) for p in source.rglob('*') if p.is_file()}
    if files != set(MEMBERS):
        raise ValueError('source member inventory mismatch')
    for name, (src_hash, rb_hash) in MEMBERS.items():
        src = checked(source / name, src_hash)
        rb = checked(proof / 'epf-readback' / name, rb_hash)
        if name.endswith('Module.bsl') and src.decode('utf-8-sig').replace('\r\n', '\n') != rb.decode('utf-8-sig').replace('\r\n', '\n'):
            raise ValueError('native BSL readback mismatch')
    artifact = pack['artifact']
    if Path(artifact['path']).name != 'MCPConnector.epf' or artifact['bytes'] != 22397 or artifact['sha256'] != '50ec8a4db3e7fdb94d062e6b84938ca71498fedd157e967229af811739a611b4':
        raise ValueError('EPF binding mismatch')
    assets = {'MCPConnector.epf': checked(proof / 'artifacts/MCPConnector.epf', artifact['sha256'], artifact['bytes'])}
    for edition in ('community', 'advanced', 'pro'):
        entry = build['binaries'][edition]
        filename = Path(entry['path']).name
        expected = {'community': 'mcp-1c-community-graph', 'advanced': 'mcp-1c-advanced-graph-v6', 'pro': 'mcp-1c-pro-graph-v6'}[edition]
        if filename != expected:
            raise ValueError('server basename mismatch')
        assets[filename] = checked(proof / 'bin' / filename, entry['sha256'], entry['bytes'])
    manifest = {
        'status': 'LOCAL_ONLY_UNPUBLISHABLE', 'platform': 'darwin-arm64',
        'connector': {'source_commit': pack['source'], 'binary_sha256': artifact['sha256'], 'native_pack_readback': 'PASS', 'bsl_readback': 'equal_normalized', 'all_source_members': MEMBERS, 'xml_readback': 'pinned_bytes_not_semantic_comparison'},
        'servers': {e: {'file': Path(build['binaries'][e]['path']).name, 'sha256': build['binaries'][e]['sha256'], 'source': pack['source'] if e == 'community' else build['tree'], 'proof': 'historical_build_only', 'authority': 'synthetic_test_issuer' if e != 'community' else 'community_no_paid_grant'} for e in ('community', 'advanced', 'pro')},
        'limitations': ['Graph historical candidate; Advanced/Pro dirty local replace/vendor and synthetic test issuer', 'not current 541ee source', 'no release, install, update, rollback, RIB or other platform acceptance'],
        'proof_packet_sha256': PACKETS,
    }
    output.mkdir(mode=0o700)
    try:
        for name, data in assets.items():
            with (output / name).open('xb') as f:
                f.write(data)
            if name != 'MCPConnector.epf':
                (output / name).chmod(0o700)
        with (output / 'manifest.json').open('x') as f:
            json.dump(manifest, f, indent=2, sort_keys=True)
            f.write('\n')
        with (output / 'SHA256SUMS').open('x') as f:
            for name in sorted(assets):
                f.write(f'{digest(assets[name])}  {name}\n')
            f.write(f"{digest((output / 'manifest.json').read_bytes())}  manifest.json\n")
    except BaseException:
        shutil.rmtree(output)
        raise

def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--output', type=Path, required=True, help='new local directory')
    p.add_argument('--source', type=Path, default=ROOT / 'epf/source')
    p.add_argument('--proof', type=Path, default=PROOF)
    args = p.parse_args()
    try:
        stage(args.output, args.source, args.proof)
    except (OSError, ValueError, KeyError, UnicodeError) as exc:
        print(f'local staging refused: {exc}', file=sys.stderr)
        return 1
    print(f'LOCAL_ONLY_UNPUBLISHABLE: {args.output}')
    return 0

if __name__ == '__main__':
    sys.exit(main())
