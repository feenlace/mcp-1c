import importlib.util
from pathlib import Path
import shutil
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('stage_local_rc', Path(__file__).with_name('stage_local_rc.py'))
stage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(stage)

class StageChecks(unittest.TestCase):
    def test_missing_altered_and_existing_output(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / 'source'
            shutil.copytree(stage.ROOT / 'epf/source', source)
            out = root / 'out'
            member = source / 'MCPConnector.xml'
            data = member.read_bytes()
            member.unlink()
            with self.assertRaises(ValueError):
                stage.stage(out, source, stage.PROOF)
            self.assertFalse(out.exists())
            member.write_bytes(data + b'changed')
            with self.assertRaises(ValueError):
                stage.stage(out, source, stage.PROOF)
            self.assertFalse(out.exists())
            member.write_bytes(data)
            proof = root / 'proof'
            proof.mkdir()
            for name in ('public', 'epf-readback', 'bin'):
                (proof / name).symlink_to(stage.PROOF / name, target_is_directory=True)
            (proof / 'artifacts').mkdir()
            artifact = proof / 'artifacts/MCPConnector.epf'
            with self.assertRaises(FileNotFoundError):
                stage.stage(out, source, proof)
            self.assertFalse(out.exists())
            artifact.write_bytes(b'altered')
            with self.assertRaises(ValueError):
                stage.stage(out, source, proof)
            self.assertFalse(out.exists())
            out.mkdir()
            marker = out / 'keep'
            marker.write_bytes(b'unchanged')
            with self.assertRaises(FileExistsError):
                stage.stage(out, source, stage.PROOF)
            self.assertEqual(marker.read_bytes(), b'unchanged')
            self.assertEqual(list(out.iterdir()), [marker])

if __name__ == '__main__':
    unittest.main()
