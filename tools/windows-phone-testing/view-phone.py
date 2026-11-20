"""Read-only local view of the physical iPhone. Claims the phone while running."""
from http.server import BaseHTTPRequestHandler, HTTPServer
import importlib.util
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parent
HOST = '127.0.0.1'
PORT = 6081
FRAME_TIMEOUT = 65
PNG_SIGNATURE = b'\x89PNG\r\n\x1a\n'
SESSION_PATH = ROOT / 'phone-session.py'
spec = importlib.util.spec_from_file_location('phone_session', SESSION_PATH)
session = importlib.util.module_from_spec(spec)
spec.loader.exec_module(session)

HTML = b'''<!doctype html>
<meta name="viewport" content="width=device-width"><title>iPhone viewer</title>
<style>body{background:#141414;color:#eee;font:16px system-ui;text-align:center}img{max-height:85vh;max-width:95vw}p{color:#aaa}</style>
<h1>iPhone viewer</h1><p id="status">Connecting to the board...</p><img id="phone" alt="">
<script>
const phone = document.getElementById('phone');
const status = document.getElementById('status');
let url;
function clearFrame() {
  phone.removeAttribute('src');
  if (url) URL.revokeObjectURL(url);
  url = undefined;
}
async function next() {
  try {
    const response = await fetch('/frame', {cache: 'no-store', signal: AbortSignal.timeout(70000)});
    if (!response.ok) throw Error(await response.text());
    const frame = await response.blob();
    clearFrame();
    url = URL.createObjectURL(frame);
    phone.src = url;
    status.textContent = 'Frame received at ' + new Date().toLocaleTimeString();
  } catch (error) {
    clearFrame();
    status.textContent = error.message;
  } finally {
    setTimeout(next, 2000);
  }
}
next();
</script>'''


def capture_frame():
    session.require_owned_context()
    session.verify_candidate()
    result = subprocess.run(
        [str(session.CLI), 'screenshot', '--output', '-'],
        env=session.cli_environment(), capture_output=True, timeout=FRAME_TIMEOUT,
        creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0),
    )
    if result.returncode or not result.stdout.startswith(PNG_SIGNATURE):
        raise RuntimeError('Screen unavailable. Check that iPhone Screen Mirroring is active.')
    return result.stdout


class Handler(BaseHTTPRequestHandler):
    def setup(self):
        self.request.settimeout(5)
        super().setup()

    def log_message(self, *_):
        pass

    def reply(self, status, content_type, data):
        self.send_response(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(data)))
        self.send_header('Cache-Control', 'no-store')
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.headers.get('Host') != f'{HOST}:{PORT}':
            self.send_error(403)
            return
        if self.path == '/':
            self.reply(200, 'text/html; charset=utf-8', HTML)
        elif self.path == '/frame':
            try:
                frame = capture_frame()
            except (OSError, RuntimeError, subprocess.SubprocessError):
                self.reply(503, 'text/plain; charset=utf-8', b'Screen unavailable. Check the phone viewer session and Screen Mirroring.')
                return
            self.reply(200, 'image/png', frame)
        else:
            self.send_error(404)


def serve():
    session.require_owned_context()
    session.require_candidate()
    with HTTPServer((HOST, PORT), Handler) as server:
        print(f'iPhone viewer: http://{HOST}:{PORT}/ (Ctrl+C to close)', flush=True)
        server.serve_forever()


if __name__ == '__main__':
    try:
        if sys.argv[1:] == ['--serve']:
            serve()
        elif sys.argv[1:]:
            sys.exit('Usage: python view-phone.py')
        else:
            sys.exit(session.run([sys._base_executable, __file__, '--serve']))
    except session.SessionBusy as exc:
        print(exc, file=sys.stderr)
        sys.exit(75)
    except (OSError, ValueError, session.SessionError) as exc:
        sys.exit(str(exc))
