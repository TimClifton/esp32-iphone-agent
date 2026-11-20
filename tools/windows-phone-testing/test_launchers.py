"""Launcher tests using mock IPC and dummy processes. No board access."""
import importlib.util
import ctypes
import io
import json
import os
from pathlib import Path
import subprocess
import select
import socket
import struct
import sys
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch

SCRIPT = Path(__file__).with_name('phone-session.py')


def read_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


session = read_module('phone_session', SCRIPT)
viewer = read_module('phone_view', SCRIPT.with_name('view-phone.py'))


class LauncherTests(unittest.TestCase):
    def test_unowned_controller_is_rejected_before_cli(self):
        with patch.dict(os.environ, {session.LEASE_CONTEXT: ''}), patch.object(session.subprocess, 'call') as command:
            with self.assertRaisesRegex(session.SessionError, 'Start a phone session'):
                session.main(['--cli', 'device', 'info'])
            with self.assertRaises(session.SessionError):
                session.main(['--check'])
            command.assert_not_called()

    def test_missing_candidate_never_falls_back(self):
        with tempfile.TemporaryDirectory() as folder:
            original = Path(folder) / 'esp32agent.exe'
            original.touch()
            with patch.object(session, 'CLI', original.with_name('esp32agent-local6.exe')), patch.object(session.subprocess, 'call') as command:
                with self.assertRaisesRegex(session.SessionError, 'Required phone CLI is missing'):
                    session.main(['--cli', 'version'])
                command.assert_not_called()

    def test_pure_version_preserves_native_exit_code(self):
        with patch.object(session, 'require_candidate'), patch.object(session.subprocess, 'call', return_value=23) as command:
            self.assertEqual(session.main(['--cli', 'version']), 23)
            self.assertEqual(command.call_args.args[0], [str(session.CLI), 'version'])
            self.assertEqual(command.call_args.kwargs['env']['ESP32AGENT_REQUIRE_LIVE'], '1')

    @unittest.skipUnless(os.name == 'nt', 'Windows process ownership')
    def test_owned_context_expires_with_lock(self):
        with tempfile.TemporaryDirectory() as folder:
            profile = Path(folder)
            config = {session.LEASE_CONTEXT: 'fixture-job'}
            with patch.object(session, 'PROFILE', profile), patch.dict(os.environ, config), patch.object(session, 'in_windows_job', return_value=True):
                with session.Lease(profile / 'control.lock') as lease:
                    lease.publish({'job': config[session.LEASE_CONTEXT], 'pid': os.getpid()})
                    session.require_owned_context()
                with self.assertRaises(session.SessionError):
                    session.require_owned_context()

    def test_wrong_version_blocks_board_command(self):
        legacy = b'{"status":"ok","data":{"version":"0.1.4"}}\n'
        with patch.object(session, 'require_owned_context'), patch.object(session, 'require_candidate'), patch.object(session.subprocess, 'run', return_value=Mock(returncode=0, stdout=legacy)) as version, patch.object(session.subprocess, 'call') as command:
            with self.assertRaisesRegex(session.SessionError, 'expected version'):
                session.main(['--cli', 'device', 'info'])
            self.assertEqual(version.call_args.args[0], [str(session.CLI), 'version'])
            command.assert_not_called()

    def test_matching_version_allows_leased_board_command(self):
        reply = json.dumps({'status': 'ok', 'data': {'version': session.VERSION}}).encode('ascii')
        with patch.object(session, 'require_owned_context'), patch.object(session, 'require_candidate'), patch.object(session.subprocess, 'run', return_value=Mock(returncode=0, stdout=reply)), patch.object(session.subprocess, 'call', return_value=42) as command:
            self.assertEqual(session.main(['--cli', 'device', 'info']), 42)
            self.assertEqual(command.call_args.args[0], [str(session.CLI), 'device', 'info'])

    @unittest.skipUnless(os.name == 'nt', 'Windows process ownership')
    def test_supervisor_rejects_wrong_version_before_daemon_or_controller(self):
        with tempfile.TemporaryDirectory() as folder, patch.object(session, 'PROFILE', Path(folder)), patch.object(session, 'verify_candidate', side_effect=session.SessionError('unexpected version')), patch.object(session, 'verify_available') as preflight, patch.object(session.subprocess, 'Popen') as controller:
            with self.assertRaisesRegex(session.SessionError, 'unexpected version'):
                session.run(['dummy'])
            preflight.assert_not_called()
            controller.assert_not_called()

    def test_daemon_stop_withheld_without_matching_process(self):
        job = Mock()
        job.owns.return_value = False
        metadata = {'version': session.VERSION, 'pid': 123}
        with patch.object(session, 'daemon_request', return_value=metadata) as request, patch.object(session, 'WindowsProcess', create=True) as process:
            process.return_value.__enter__.return_value = Mock()
            with self.assertRaisesRegex(session.SessionError, 'does not belong'):
                session.stop_daemon(session.PROFILE, job)
        request.assert_called_once_with(session.PROFILE, 'version')

    def test_daemon_identity_and_exit_verified_after_stop_ack(self):
        metadata = {'version': session.VERSION, 'pid': 123}
        replies = [metadata, {'message': 'daemon stopping'}]
        with patch.object(session, 'daemon_request', side_effect=replies) as request, patch.object(session, 'WindowsProcess') as process:
            owned = process.return_value.__enter__.return_value
            owned.image.return_value = session.CLI.resolve()
            owned.exited.return_value = False
            with self.assertRaisesRegex(session.SessionError, 'did not exit'):
                session.stop_daemon(session.PROFILE, Mock())
            owned.exited.assert_called_once_with(8000)
        self.assertEqual([call.args[1] for call in request.call_args_list], ['version', 'stop'])

    def test_preflight_rejects_preexisting_daemon(self):
        with patch.object(session, 'daemon_request', return_value={'version': session.VERSION, 'pid': 123}):
            with self.assertRaisesRegex(session.SessionError, 'still running outside this session'):
                session.verify_available(session.PROFILE)

    def test_direct_ipc_writes_only_the_local_request(self):
        with tempfile.TemporaryDirectory() as folder, patch.object(session.socket, 'socket') as local_socket, patch.object(session, 'connect_local') as connect:
            profile = Path(folder)
            (profile / 'daemon.sock').touch()
            connection = local_socket.return_value.__enter__.return_value
            reply = {'status': 'ok', 'data': {'version': session.VERSION, 'pid': 123}}
            connection.makefile.return_value = io.BytesIO(json.dumps(reply).encode('ascii') + b'\n')
            self.assertEqual(session.daemon_request(profile, 'version'), reply['data'])
            connect.assert_called_once_with(connection, profile / 'daemon.sock', 2)
            self.assertEqual(json.loads(connection.sendall.call_args.args[0]), {'cmd': 'version'})

    def test_viewer_requires_lease_and_uses_only_png_stdout(self):
        expected = viewer.PNG_SIGNATURE + b'fixture'
        with patch.object(viewer.session, 'require_owned_context') as check, patch.object(viewer.session, 'verify_candidate'), patch.object(viewer.subprocess, 'run') as command:
            command.return_value = Mock(returncode=0, stdout=expected)
            self.assertEqual(viewer.capture_frame(), expected)
            check.assert_called_once()
            self.assertEqual(command.call_args.args[0][-3:], ['screenshot', '--output', '-'])
            self.assertGreater(command.call_args.kwargs['timeout'], 45)
            command.return_value = Mock(returncode=1, stdout=expected)
            with self.assertRaisesRegex(RuntimeError, 'Screen unavailable'):
                viewer.capture_frame()
            command.return_value = Mock(returncode=0, stdout=b'{"status":"error"}')
            with self.assertRaises(RuntimeError):
                viewer.capture_frame()


@unittest.skipUnless(os.name == 'nt', 'Windows process ownership')
class WindowsJobTests(unittest.TestCase):
    def test_outside_process_cannot_use_genuine_active_lease_context(self):
        with tempfile.TemporaryDirectory() as folder, session.WindowsJob() as job:
            profile = Path(folder)
            with session.Lease(profile / 'control.lock') as lease:
                lease.publish({'job': job.name, 'pid': os.getpid()})
                with patch.object(session, 'PROFILE', profile), patch.dict(os.environ, {session.LEASE_CONTEXT: job.name}):
                    with self.assertRaisesRegex(session.SessionError, 'Start a phone session'):
                        session.require_owned_context()

    def test_native_af_unix_round_trip_to_fixture(self):
        with tempfile.TemporaryDirectory(prefix='ipc-', dir=SCRIPT.parent) as folder:
            profile = Path(folder)
            path = profile / 'daemon.sock'
            winsock = ctypes.WinDLL('ws2_32')
            bind = winsock.bind
            bind.argtypes = [ctypes.c_size_t, ctypes.c_char_p, ctypes.c_int]
            accept = winsock.accept
            accept.argtypes = [ctypes.c_size_t, ctypes.c_void_p, ctypes.c_void_p]
            accept.restype = ctypes.c_size_t
            address = struct.pack('=H108s', 1, str(path).encode('utf-8'))
            requests = []
            with socket.socket(1, socket.SOCK_STREAM) as listener:
                result = bind(listener.fileno(), address, len(address))
                self.assertEqual(result, 0)
                listener.settimeout(3)
                listener.listen(1)

                def respond():
                    ready, _, _ = select.select([listener], [], [], 3)
                    if not ready:
                        return
                    handle = accept(listener.fileno(), None, None)
                    with socket.socket(1, socket.SOCK_STREAM, fileno=handle) as peer, peer.makefile('rb') as body:
                        peer.settimeout(3)
                        requests.append(json.loads(body.readline(4096)))
                        peer.sendall(b'{"status":"ok","data":{"version":"fixture","pid":123}}\n')

                worker = threading.Thread(target=respond)
                worker.start()
                try:
                    reply = session.daemon_request(profile, 'version')
                    self.assertEqual(reply, {'version': 'fixture', 'pid': 123})
                finally:
                    worker.join(timeout=4)
            self.assertEqual(requests, [{'cmd': 'version'}])
            self.assertIsNone(session.daemon_request(profile, 'version'))

    def test_supervisor_passes_stdio_status_and_releases_fixture_lease(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            fixture = root / SCRIPT.name
            fixture.write_bytes(SCRIPT.read_bytes())
            supervisor = '''
import importlib.util, sys
s = importlib.util.spec_from_file_location('runner', sys.argv[1]); m = importlib.util.module_from_spec(s); s.loader.exec_module(m)
m.verify_candidate = lambda: None
sys.exit(m.run(sys.argv[2:]))
'''
            command = '''
import subprocess, sys
subprocess.run([sys._base_executable, sys.argv[1], '--check'], check=True)
print(sys.stdin.readline().strip())
sys.exit(23)
'''
            result = subprocess.run(
                [sys._base_executable, '-c', supervisor, str(fixture), sys._base_executable, '-c', command, str(fixture)],
                input='stdin preserved\n', capture_output=True, text=True, timeout=15,
            )
            self.assertEqual(result.returncode, 23, result.stderr)
            self.assertEqual(result.stdout.strip(), 'stdin preserved')
            with session.Lease(root / 'real-board-state/control.lock'):
                pass

    def test_owner_death_terminates_dummy_process_tree(self):
        code = '''
import importlib.util, subprocess, sys, time
s = importlib.util.spec_from_file_location('runner', sys.argv[1]); m = importlib.util.module_from_spec(s); s.loader.exec_module(m)
with m.WindowsJob() as job:
    dummy = subprocess.Popen([sys._base_executable, '-c', 'import time; time.sleep(30)'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        job.add(dummy)
        print(dummy.pid, flush=True)
        time.sleep(30)
    finally:
        dummy.kill(); dummy.wait(timeout=5)
'''
        owner = subprocess.Popen([sys._base_executable, '-c', code, str(SCRIPT)], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            pid = owner.stdout.readline().strip()
            if not pid:
                owner.wait(timeout=5)
                self.fail(owner.stderr.read())
            with session.WindowsProcess(int(pid)) as dummy:
                self.assertEqual(dummy.image(), Path(sys._base_executable).resolve())
                self.assertFalse(dummy.exited(0))
                owner.kill()
                owner.wait(timeout=5)
                self.assertTrue(dummy.exited(5000))
        finally:
            if owner.poll() is None:
                owner.kill()
                owner.wait(timeout=5)
            owner.stdout.close()
            owner.stderr.close()


if __name__ == '__main__':
    unittest.main()
