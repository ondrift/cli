import importlib.util, json, os, sys, threading, time, urllib.request

FUNCTION_NAME    = "{{FUNCTION_NAME}}"
FUNCTION_METHOD  = "{{FUNCTION_METHOD}}"
FUNCTION_AUTH    = "{{FUNCTION_AUTH}}"
FUNCTION_ELEMENT = "{{FUNCTION_ELEMENT}}"

_dir = os.path.dirname(os.path.abspath(__file__))
_vendor = os.path.join(_dir, "vendor")
if os.path.isdir(_vendor):
    sys.path.insert(0, _vendor)

registered_path = f"{FUNCTION_ELEMENT}/{FUNCTION_NAME}" if FUNCTION_ELEMENT else FUNCTION_NAME

_spec = importlib.util.spec_from_file_location("handler", os.path.join(_dir, "handler.py"))
_mod  = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_mod)

PROXY_URL = os.environ["ATOMIC_PROXY_URL"]
PORT      = int(os.environ.get("PORT", "8000"))

# ===== Log shipping =====
_log_buf  = []
_log_lock = threading.Lock()

def _log(line):
    ts = time.strftime("%Y-%m-%dT%H:%M:%S")
    sys.__stderr__.write(f"{ts}  {line}\n")
    sys.__stderr__.flush()
    with _log_lock:
        _log_buf.append(line)

def _flush_logs():
    while True:
        time.sleep(1)
        with _log_lock:
            if not _log_buf:
                continue
            batch, _log_buf[:] = _log_buf[:], []
        payload = json.dumps({"function": registered_path, "lines": batch}).encode()
        try:
            req = urllib.request.Request(
                f"{PROXY_URL}/logs/ingest", data=payload,
                headers={"Content-Type": "application/json"},
            )
            urllib.request.urlopen(req, timeout=5)
        except Exception:
            pass

# ===== Self-registration =====
def _register():
    hostname = os.environ.get("HOSTNAME", "")
    service  = "-".join(hostname.split("-")[:2])
    address  = f"http://{service}:8000"
    payload  = json.dumps({
        "url": address, "path": registered_path,
        "method": FUNCTION_METHOD, "auth": FUNCTION_AUTH,
    }).encode()
    for attempt in range(10):
        try:
            req = urllib.request.Request(
                f"{PROXY_URL}/register", data=payload,
                headers={"Content-Type": "application/json"},
            )
            with urllib.request.urlopen(req, timeout=10) as resp:
                if resp.status == 200:
                    _log(f"Registered at {PROXY_URL}")
                    return
        except Exception as e:
            _log(f"Registration attempt {attempt + 1} failed: {e}")
            time.sleep(1)
    _log("Registration failed after 10 attempts")
    sys.exit(1)

# ===== HTTP server =====
from http.server import HTTPServer, BaseHTTPRequestHandler

class _Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path.rstrip("/") != f"/{FUNCTION_NAME}":
            self.send_error(404)
            return
        length = int(self.headers.get("Content-Length", 0))
        body   = json.loads(self.rfile.read(length)) if length else {}
        try:
            status, message, data = getattr(_mod, "{{FUNC}}")(body)
        except Exception as e:
            _log(f"Handler error: {e}")
            self.send_error(500)
            return
        resp = json.dumps({"status": status, "message": message, "payload": data}).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(resp)))
        self.end_headers()
        self.wfile.write(resp)

    def log_message(self, fmt, *args):
        _log(fmt % args)

if __name__ == "__main__":
    _register()
    threading.Thread(target=_flush_logs, daemon=True).start()
    _log(f"Python POST server on :{PORT}")
    HTTPServer(("", PORT), _Handler).serve_forever()
