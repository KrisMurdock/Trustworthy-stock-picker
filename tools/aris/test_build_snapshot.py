import gzip
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

spec=importlib.util.spec_from_file_location('build_snapshot',Path(__file__).with_name('build_snapshot.py'))
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class SnapshotTests(unittest.TestCase):
    def test_strips_metadata_and_retains_capacity_units(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            with gzip.open(root/'sz000001.json.gz','wt') as f:
                json.dump({'source':'fixture','bars':[{'date':'2026-01-02','open':10,'high':11,'low':9,'close':10,'volume_raw':123}]},f)
            with gzip.open(root/'sz000001-capacity.json.gz','wt') as f:
                json.dump([['2026-01-02',11000000,456,'fixture']],f)
            signal={'code':'sz000001','date':'2026-01-02','atr':.04,'cross_date':'2025-12-25'}
            snapshot,signals=module.build(root,root,[signal],['2026-01-02'])
            self.assertEqual(set(signals[0]),{'code','date','atr'})
            self.assertIn('cross_date',signal)
            self.assertEqual(snapshot['stocks']['sz000001'][1][2:7],[11000000]*4+[456])
