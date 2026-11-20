import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

path=Path(__file__).with_name('phone-session.py')
spec=importlib.util.spec_from_file_location('phone_session',path)
session=importlib.util.module_from_spec(spec);spec.loader.exec_module(session)

class LeaseTests(unittest.TestCase):
    def test_conflict_and_release(self):
        with tempfile.TemporaryDirectory(dir=Path(__file__).parent) as tmp:
            lock=Path(tmp)/'control.lock'
            with session.Lease(lock):
                with self.assertRaises(session.SessionBusy):
                    with session.Lease(lock): pass
            with session.Lease(lock): pass

    def test_process_exit_releases_lease(self):
        with tempfile.TemporaryDirectory(dir=Path(__file__).parent) as tmp:
            lock=Path(tmp)/'control.lock'
            code="import importlib.util,sys,time; s=importlib.util.spec_from_file_location('p',sys.argv[1]); m=importlib.util.module_from_spec(s); s.loader.exec_module(m); l=m.Lease(sys.argv[2]); l.__enter__(); print('locked',flush=True); time.sleep(60)"
            child=subprocess.Popen([sys._base_executable,'-c',code,str(path),str(lock)],stdout=subprocess.PIPE,text=True)
            try:
                self.assertEqual(child.stdout.readline().strip(),'locked')
                with self.assertRaises(session.SessionBusy):
                    with session.Lease(lock): pass
            finally:
                child.kill();child.wait(timeout=5);child.stdout.close()
            with session.Lease(lock): pass

if __name__=='__main__': unittest.main()
