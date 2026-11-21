"""Own the local phone lease and all processes created during a session."""
import ctypes
import errno
import json
import os
from pathlib import Path
import select
import socket
import struct
import subprocess
import sys
import time
from ctypes import wintypes
from uuid import uuid4

ROOT = Path(__file__).resolve().parent
PROFILE = ROOT / 'real-board-state'
CLI = ROOT / 'cli/esp32agent-local15.exe'
VERSION = '0.1.4-local-pointer15'
LEASE_CONTEXT = 'IOS_LAB_PHONE_LEASE_CONTEXT'


class SessionError(RuntimeError):
    pass


class SessionBusy(SessionError):
    pass


class Lease:
    def __init__(self, path):
        self.path = Path(path)
        self.file = None

    def __enter__(self):
        self.path.parent.mkdir(parents=True, exist_ok=True)
        fd = os.open(self.path, os.O_RDWR | os.O_CREAT | getattr(os, 'O_BINARY', 0), 0o600)
        self.file = os.fdopen(fd, 'r+b', buffering=0)
        try:
            if os.name == 'nt':
                import msvcrt
                msvcrt.locking(self.file.fileno(), msvcrt.LK_NBLCK, 1)
            else:
                import fcntl
                fcntl.flock(self.file.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError as exc:
            self.file.close()
            self.file = None
            raise SessionBusy('Phone is already claimed by another local session.') from exc
        return self

    def publish(self, record):
        # Byte zero is the OS lock. The remaining bytes identify its current owner.
        self.file.seek(1)
        self.file.write(json.dumps(record).encode('ascii'))
        self.file.truncate()

    def __exit__(self, *_):
        self.file.close()
        self.file = None


def session_record():
    try:
        with (PROFILE / 'control.lock').open('rb') as lock:
            lock.seek(1)
            record = json.loads(lock.read(4096))
            if isinstance(record, dict):
                return record
    except (OSError, ValueError):
        pass
    return {}


def require_owned_context():
    job_name = os.environ.get(LEASE_CONTEXT)
    if os.name == 'nt' and job_name:
        try:
            record = session_record()
            owner_pid = record.get('pid')
            matches_lease = record.get('job') == job_name and type(owner_pid) is int and owner_pid > 0
            if matches_lease and parent_alive(owner_pid) and in_windows_job(job_name):
                try:
                    with Lease(PROFILE / 'control.lock'):
                        pass
                except SessionBusy:
                    return
        except (OSError, ValueError):
            pass
    raise SessionError('Start a phone session before running commands against the real board.')


def parent_alive(pid):
    with WindowsProcess(pid) as process:
        return not process.exited(0)


def cli_environment():
    env = os.environ.copy()
    env.update(
        ESP32AGENT_HOME=str(PROFILE),
        ESP32AGENT_SIM=sys._base_executable,
        ESP32AGENT_SIM_SCREEN=str(ROOT / 'real-ble-bridge.py'),
        ESP32AGENT_CONNECT_TIMEOUT='45s',
        ESP32AGENT_RECORDING='off',
        ESP32AGENT_REQUIRE_LIVE='1',
        PYTHONPATH=str(ROOT / '.venv/Lib/site-packages'),
        PATH=str(ROOT / 'tools') + os.pathsep + env['PATH'],
    )
    return env


def require_candidate():
    if not CLI.is_file():
        raise SessionError(f'Required phone CLI is missing: {CLI.name} ({VERSION}).')


def verify_candidate():
    require_candidate()
    try:
        result = subprocess.run(
            [str(CLI), 'version'], env=cli_environment(), capture_output=True,
            timeout=5, creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0),
        )
        if result.returncode == 0:
            reply = json.loads(result.stdout)
            if reply == {'status': 'ok', 'data': {'version': VERSION}}:
                return
    except (OSError, ValueError, subprocess.SubprocessError):
        pass
    raise SessionError(f'Phone CLI did not report the expected version {VERSION}.')


def connect_local(connection, path, timeout):
    if os.name != 'nt':
        connection.connect(str(path))
        return
    # Windows supports AF_UNIX, but some CPython builds omit its address marshalling.
    address = str(path).encode('utf-8')
    if len(address) >= 108:
        raise SessionError('Phone profile path exceeds the Windows local socket path limit.')
    sockaddr = struct.pack('=H108s', 1, address)  # sockaddr_un: AF_UNIX and sun_path
    winsock = ctypes.WinDLL('ws2_32')
    connect = winsock.connect
    connect.argtypes = [ctypes.c_size_t, ctypes.c_char_p, ctypes.c_int]
    connect.restype = ctypes.c_int
    # settimeout() already placed the socket in non-blocking mode.
    if connect(connection.fileno(), sockaddr, len(sockaddr)) == 0:
        return
    error = winsock.WSAGetLastError()
    if error in (10035, 10036):  # WSAEWOULDBLOCK, WSAEINPROGRESS
        _, ready, failed = select.select([], [connection], [connection], timeout)
        if not ready and not failed:
            raise TimeoutError('Timed out connecting to the private phone daemon.')
        error = connection.getsockopt(socket.SOL_SOCKET, socket.SO_ERROR)
        if error == 0:
            return
    raise ctypes.WinError(error)


def daemon_request(profile, command):
    """A direct request to the private local socket. Never starts a daemon."""
    path = profile / 'daemon.sock'
    try:
        path.lstat()
    except FileNotFoundError:
        return None
    timeout = 2
    with socket.socket(getattr(socket, 'AF_UNIX', 1), socket.SOCK_STREAM) as connection:
        connection.settimeout(timeout)
        try:
            connect_local(connection, path, timeout)
        except OSError as exc:
            if exc.errno in (errno.ENOENT, errno.ECONNREFUSED, 10061) or getattr(exc, 'winerror', None) in (2, 3, 10061):
                return None
            raise
        connection.sendall(json.dumps({'cmd': command}).encode('ascii') + b'\n')
        with connection.makefile('rb') as response:
            line = response.readline(4096)
        if not line.endswith(b'\n'):
            raise SessionError(f'Phone daemon gave an incomplete {command} response.')
        result = json.loads(line)
        if not isinstance(result, dict) or result.get('status') != 'ok' or not isinstance(result.get('data'), dict):
            raise SessionError(f'Phone daemon rejected the {command} request.')
        return result['data']


def verify_available(profile):
    if daemon_request(profile, 'version') is not None:
        raise SessionError('A previous phone daemon is still running outside this session. Verify its owner and stop it before retrying.')


def stop_daemon(profile, job):
    data = daemon_request(profile, 'version')
    if data is None:
        return
    pid = data.get('pid')
    if data.get('version') != VERSION or type(pid) is not int or pid <= 0:
        raise SessionError('Phone daemon identity did not match the expected version and PID. Stop was withheld.')
    with WindowsProcess(pid) as process:
        if not job.owns(process.handle) or process.image() != CLI.resolve():
            raise SessionError('Phone daemon does not belong to this session. Stop was withheld.')
        reply = daemon_request(profile, 'stop')
        if reply is not None and reply.get('message') != 'daemon stopping':
            raise SessionError('Phone daemon did not acknowledge the stop request.')
        if not process.exited(8000):
            raise SessionError('Phone daemon did not exit after its stop request.')


class WindowsProcess:
    def __init__(self, pid):
        kernel = ctypes.WinDLL('kernel32', use_last_error=True)
        self.close = kernel.CloseHandle
        self.close.argtypes = [wintypes.HANDLE]
        self.wait = kernel.WaitForSingleObject
        self.wait.argtypes = [wintypes.HANDLE, wintypes.DWORD]
        self.wait.restype = wintypes.DWORD
        self.query = kernel.QueryFullProcessImageNameW
        self.query.argtypes = [wintypes.HANDLE, wintypes.DWORD, wintypes.LPWSTR, ctypes.POINTER(wintypes.DWORD)]
        open_process = kernel.OpenProcess
        open_process.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        open_process.restype = wintypes.HANDLE
        self.handle = open_process(0x101000, False, pid)  # SYNCHRONIZE | QUERY_LIMITED_INFORMATION
        if not self.handle:
            raise ctypes.WinError(ctypes.get_last_error())

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.close(self.handle)

    def exited(self, milliseconds):
        status = self.wait(self.handle, milliseconds)
        if status not in (0, 258):  # WAIT_OBJECT_0 or WAIT_TIMEOUT
            raise ctypes.WinError(ctypes.get_last_error())
        return status == 0

    def image(self):
        path = ctypes.create_unicode_buffer(32768)
        size = wintypes.DWORD(len(path))
        if not self.query(self.handle, 0, path, ctypes.byref(size)):
            raise ctypes.WinError(ctypes.get_last_error())
        return Path(path.value).resolve()


class JobBasicLimits(ctypes.Structure):
    _fields_ = [
        ('per_process_time', ctypes.c_longlong), ('per_job_time', ctypes.c_longlong),
        ('flags', wintypes.DWORD), ('min_working_set', ctypes.c_size_t),
        ('max_working_set', ctypes.c_size_t), ('active_process_limit', wintypes.DWORD),
        ('affinity', ctypes.c_size_t), ('priority_class', wintypes.DWORD),
        ('scheduling_class', wintypes.DWORD),
    ]


class JobExtendedLimits(ctypes.Structure):
    _fields_ = [
        ('basic', JobBasicLimits), ('io_counts', ctypes.c_ulonglong * 6),
        ('process_memory_limit', ctypes.c_size_t), ('job_memory_limit', ctypes.c_size_t),
        ('peak_process_memory', ctypes.c_size_t), ('peak_job_memory', ctypes.c_size_t),
    ]


class JobAccounting(ctypes.Structure):
    _fields_ = [
        ('times', ctypes.c_longlong * 4), ('page_faults', wintypes.DWORD),
        ('total', wintypes.DWORD), ('active', wintypes.DWORD), ('terminated', wintypes.DWORD),
    ]


class WindowsJob:
    """On supervisor death, Windows terminates all descendants in this job."""
    def __init__(self):
        self.name = f'Local\\IOSLabPhoneSession-{uuid4().hex}'

    def __enter__(self):
        kernel = ctypes.WinDLL('kernel32', use_last_error=True)
        create = kernel.CreateJobObjectW
        create.argtypes = [ctypes.c_void_p, wintypes.LPCWSTR]
        create.restype = wintypes.HANDLE
        self.close = kernel.CloseHandle
        self.close.argtypes = [wintypes.HANDLE]
        self.assign = kernel.AssignProcessToJobObject
        self.assign.argtypes = [wintypes.HANDLE, wintypes.HANDLE]
        self.terminate = kernel.TerminateJobObject
        self.terminate.argtypes = [wintypes.HANDLE, wintypes.UINT]
        self.query = kernel.QueryInformationJobObject
        self.query.argtypes = [wintypes.HANDLE, ctypes.c_int, ctypes.c_void_p, wintypes.DWORD, ctypes.c_void_p]
        set_info = kernel.SetInformationJobObject
        set_info.argtypes = [wintypes.HANDLE, ctypes.c_int, ctypes.c_void_p, wintypes.DWORD]
        ctypes.set_last_error(0)
        self.handle = create(None, self.name)
        if not self.handle:
            raise ctypes.WinError(ctypes.get_last_error())
        if ctypes.get_last_error() == 183:  # ERROR_ALREADY_EXISTS
            self.close(self.handle)
            raise SessionError('Phone session job name was already in use.')
        limits = JobExtendedLimits()
        limits.basic.flags = 0x2000  # JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        if not set_info(self.handle, 9, ctypes.byref(limits), ctypes.sizeof(limits)):
            error = ctypes.WinError(ctypes.get_last_error())
            self.close(self.handle)
            raise error
        return self

    def __exit__(self, *_):
        self.close(self.handle)

    def add(self, child):
        if not self.assign(self.handle, int(child._handle)):
            raise ctypes.WinError(ctypes.get_last_error())

    def owns(self, handle):
        return job_contains(handle, self.handle)

    def empty(self, timeout):
        deadline = time.monotonic() + timeout
        while True:
            info = JobAccounting()
            if not self.query(self.handle, 1, ctypes.byref(info), ctypes.sizeof(info), None):
                raise ctypes.WinError(ctypes.get_last_error())
            if info.active == 0:
                return True
            if time.monotonic() >= deadline:
                return False
            time.sleep(0.05)

    def abort(self):
        if not self.terminate(self.handle, 1) or not self.empty(5):
            raise SessionError('Could not verify that all owned local phone processes exited.')


def job_contains(process_handle, job_handle):
    is_in_job = ctypes.WinDLL('kernel32', use_last_error=True).IsProcessInJob
    is_in_job.argtypes = [wintypes.HANDLE, wintypes.HANDLE, ctypes.POINTER(wintypes.BOOL)]
    member = wintypes.BOOL()
    if not is_in_job(process_handle, job_handle, ctypes.byref(member)):
        raise ctypes.WinError(ctypes.get_last_error())
    return bool(member.value)


def in_windows_job(name):
    kernel = ctypes.WinDLL('kernel32', use_last_error=True)
    open_job = kernel.OpenJobObjectW
    open_job.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.LPCWSTR]
    open_job.restype = wintypes.HANDLE
    close = kernel.CloseHandle
    close.argtypes = [wintypes.HANDLE]
    current = kernel.GetCurrentProcess
    current.restype = wintypes.HANDLE
    handle = open_job(4, False, name)  # JOB_OBJECT_QUERY only
    if not handle:
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        return job_contains(current(), handle)
    finally:
        close(handle)


def run(command):
    if os.name != 'nt':
        raise SessionError('The real-board launcher requires Windows process ownership support.')
    with Lease(PROFILE / 'control.lock') as lease:
        verify_candidate()
        verify_available(PROFILE)
        with WindowsJob() as job:
            child = subprocess.Popen(
                [sys._base_executable, __file__, '--child', job.name, *command],
                creationflags=subprocess.CREATE_NEW_PROCESS_GROUP,
            )
            try:
                job.add(child)
                # The child can only start controller processes once job assignment succeeded.
                lease.publish({'pid': os.getpid(), 'job': job.name})
            except BaseException:
                child.kill()
                child.wait(timeout=5)
                raise
            try:
                result = child.wait()
            except KeyboardInterrupt:
                # Stop the command tree before allowing a concurrent controller to start.
                job.abort()
                child.wait(timeout=5)
                print('Phone session interrupted. Windows ended the owned processes.', file=sys.stderr)
                return 130
            try:
                stop_daemon(PROFILE, job)
                if not job.empty(5):
                    raise SessionError('Owned local phone processes remained after graceful shutdown.')
            except (OSError, ValueError, SessionError) as exc:
                job.abort()
                raise SessionError(f'Graceful phone cleanup could not be verified. Windows ended any remaining owned processes. {exc}') from exc
            return result


def run_child(job_name, command):
    deadline = time.monotonic() + 10
    while session_record().get('job') != job_name:
        if time.monotonic() >= deadline:
            raise SessionError('Phone session supervisor did not establish process ownership.')
        time.sleep(0.05)
    os.environ[LEASE_CONTEXT] = job_name
    require_owned_context()
    return subprocess.call(command)


def main(args):
    if args[:1] == ['--child']:
        if len(args) < 3:
            raise SessionError('The phone session supervisor must supply a context and command.')
        return run_child(args[1], args[2:])
    if args == ['--check']:
        require_owned_context()
        return 0
    if args[:1] == ['--cli']:
        commands = args[1:]
        if commands and not (len(commands) == 1 and commands[0] in ('help', '-h', '--help', 'version', '--version')):
            require_owned_context()
            if commands[0] == 'daemon':
                raise SessionError('The phone session manages daemon startup and shutdown.')
            verify_candidate()
        else:
            require_candidate()
        return subprocess.call([str(CLI), *commands], env=cli_environment())
    if args[:1] == ['--']:
        args = args[1:]
    if not args:
        raise SessionError('A phone session command is required.')
    return run(args)


if __name__ == '__main__':
    try:
        sys.exit(main(sys.argv[1:]))
    except SessionBusy as exc:
        print(exc, file=sys.stderr)
        sys.exit(75)
    except (OSError, ValueError, SessionError) as exc:
        print(exc, file=sys.stderr)
        sys.exit(1)
