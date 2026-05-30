#!/usr/bin/env python3
"""Polyglot Chirp pod — a sov mesh member written in pure-stdlib Python.

This is the producer-polyglot proof: a NON-Go process that speaks the sov
wire contract (docs/WIRE_CONTRACT.md) well enough to drop into the chirp
mesh in place of the Go `cmd/mesh/chirps` pod. Same gateway, same
walkthrough, equivalent output — the mesh is language-agnostic on the
producer side, not just the consumer (generated client) side.

It implements exactly what a pod owes the mesh:
  - registers + heartbeats to the gateway with an HMAC-signed _register
  - serves POST /rpc/Chirp/{post,delete,list,get,listByAuthors} in BOTH
    arg shapes ({"args":{...}} and {"args":[...]})
  - serves /rpc/_introspect and /rpc/_health
  - verifies X-Sov-Seal on inbound to read the authenticated subject

Stdlib only — no pip install. Run via env vars (mirrors the Go pod):

  SOV_LISTEN=:9002 SOV_ADVERTISE=http://localhost:9002 \
  SOV_GATEWAY=http://localhost:8080 \
  SOV_HMAC_SECRET=demo-only-secret SOV_MESH_SECRET=demo-only-mesh-secret \
  python3 examples/chirp/polyglot/chirp_pod.py
"""
import hashlib
import hmac
import json
import os
import threading
import time
import urllib.request
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def env(key, default):
    v = os.environ.get(key)
    return v if v else default


LISTEN = env("SOV_LISTEN", ":9002")
ADVERTISE = env("SOV_ADVERTISE", "http://localhost:9002")
GATEWAY = env("SOV_GATEWAY", "http://localhost:8080").rstrip("/")
HMAC_SECRET = env("SOV_HMAC_SECRET", "demo-only-secret").encode()
MESH_SECRET = env("SOV_MESH_SECRET", "demo-only-mesh-secret").encode()
HEARTBEAT_SECONDS = 5
SERVICE = "Chirp"


# ---- in-memory chirp store -------------------------------------------------

_lock = threading.Lock()
_chirps = {}        # id -> chirp dict
_order = []         # ids newest-last
_seq = [0]


def new_id():
    _seq[0] += 1
    return f"py{_seq[0]:08x}{int(time.time()*1000)%100000:05d}"


def now_iso():
    # RFC3339 / Go time.Time JSON shape, UTC with trailing Z.
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%fZ")


def store_insert(c):
    with _lock:
        _chirps[c["id"]] = c
        _order.append(c["id"])


def store_list(limit):
    with _lock:
        ids = list(reversed(_order))[:limit]
        return [_chirps[i] for i in ids]


def store_by_authors(author_ids, limit):
    aset = set(author_ids)
    with _lock:
        out = [_chirps[i] for i in reversed(_order) if _chirps[i]["author_id"] in aset]
    return out[:limit]


def store_get(cid):
    with _lock:
        return _chirps.get(cid)


def store_delete(cid):
    with _lock:
        if cid not in _chirps:
            return False
        del _chirps[cid]
        _order.remove(cid)
        return True


# ---- wire helpers ----------------------------------------------------------

class RPCError(Exception):
    def __init__(self, status, code, message):
        self.status = status
        self.code = code
        self.message = message


def parse_args(raw_body, positional_names):
    """Decode {"args":...} into a named dict, honoring both wire shapes.

    positional_names maps array index -> field name so positional calls
    bind correctly (contract §1).
    """
    if not raw_body:
        return {}
    doc = json.loads(raw_body)
    args = doc.get("args") if isinstance(doc, dict) else None
    if args is None:
        return {}
    if isinstance(args, dict):
        return args
    if isinstance(args, list):
        # Single-object-in-array is treated as named (contract §1).
        if len(args) == 1 and isinstance(args[0], dict):
            return args[0]
        out = {}
        for i, v in enumerate(args):
            if i < len(positional_names):
                out[positional_names[i]] = v
        return out
    return {}


def seal_subject(headers):
    """Verify X-Sov-Seal over the inbound x-sov-* bundle; return the
    authenticated subject or None. Mirrors hmacseal/proto.Verify: HMAC over
    sorted, lowercased "name=value\\n" lines for every x-sov-* header except
    the seal itself (contract §4)."""
    pairs = []
    got_seal = None
    for name, value in headers.items():
        lname = name.lower()
        if not lname.startswith("x-sov-"):
            continue
        if lname == "x-sov-seal":
            got_seal = value
            continue
        pairs.append((lname, value))
    if got_seal is None:
        return None
    pairs.sort(key=lambda kv: kv[0])
    canonical = "".join(f"{k}={v}\n" for k, v in pairs).encode()
    want = hmac.new(HMAC_SECRET, canonical, hashlib.sha256).hexdigest()
    if not hmac.compare_digest(want, got_seal):
        return None
    return headers.get("X-Sov-Subject")


# ---- method handlers -------------------------------------------------------

def require_subject(headers):
    sub = seal_subject(headers)
    if not sub:
        raise RPCError(401, "UNAUTHORIZED", "authentication required")
    return sub


def m_post(headers, a):
    uid = require_subject(headers)
    body = a.get("body") or ""
    if body == "":
        raise RPCError(400, "BAD_REQUEST", "body required")
    if len(body) > 280:
        raise RPCError(400, "BAD_REQUEST", "body too long (max 280)")
    c = {"id": new_id(), "author_id": uid, "body": body, "posted_at": now_iso()}
    store_insert(c)
    return c


def m_delete(headers, a):
    require_subject(headers)
    cid = a.get("id") or ""
    if cid == "":
        raise RPCError(400, "BAD_REQUEST", "id required")
    if not store_delete(cid):
        raise RPCError(404, "NOT_FOUND", f"chirp {cid!r} not found")
    return {"ok": True}


def m_list(headers, a):
    limit = a.get("limit") or 0
    if limit <= 0 or limit > 200:
        limit = 50
    return {"chirps": store_list(limit)}


def m_get(headers, a):
    cid = a.get("id") or ""
    if cid == "":
        raise RPCError(400, "BAD_REQUEST", "id required")
    c = store_get(cid)
    if c is None:
        raise RPCError(404, "NOT_FOUND", f"chirp {cid!r} not found")
    return c


def m_list_by_authors(headers, a):
    ids = a.get("author_ids") or []
    if not ids:
        return {"chirps": []}
    limit = a.get("limit") or 0
    if limit <= 0 or limit > 200:
        limit = 50
    return {"chirps": store_by_authors(ids, limit)}


# method -> (handler, positional field order)
METHODS = {
    "post": (m_post, ["body"]),
    "delete": (m_delete, ["id"]),
    "list": (m_list, ["limit"]),
    "get": (m_get, ["id"]),
    "listByAuthors": (m_list_by_authors, ["author_ids", "limit"]),
}


# ---- introspect / health payloads ------------------------------------------

def _param(json_name, schema_type, required, position):
    return {"jsonName": json_name, "schemaType": schema_type,
            "required": required, "position": position}


INTROSPECT = {
    "services": {
        "Chirp": [{
            "router": "Chirp",
            "title": "Chirp",
            "methods": [
                {"method": "post", "title": "Post", "postPath": "/rpc/Chirp/post",
                 "hasParams": True, "params": [_param("body", "string", True, 0)],
                 "requestTypeScript": "", "responseTypeScript": ""},
                {"method": "delete", "title": "Delete", "postPath": "/rpc/Chirp/delete",
                 "hasParams": True, "params": [_param("id", "string", True, 0)],
                 "requestTypeScript": "", "responseTypeScript": ""},
                {"method": "list", "title": "List", "postPath": "/rpc/Chirp/list",
                 "hasParams": True, "params": [_param("limit", "number", False, 0)],
                 "requestTypeScript": "", "responseTypeScript": ""},
                {"method": "get", "title": "Get", "postPath": "/rpc/Chirp/get",
                 "hasParams": True, "params": [_param("id", "string", True, 0)],
                 "requestTypeScript": "", "responseTypeScript": ""},
                {"method": "listByAuthors", "title": "List By Authors",
                 "postPath": "/rpc/Chirp/listByAuthors", "hasParams": True,
                 "params": [_param("author_ids", "array", True, 0),
                            _param("limit", "number", False, 1)],
                 "requestTypeScript": "", "responseTypeScript": ""},
            ],
        }],
    },
    "types": {},
    "cross_refs": {},
}


def health_body():
    return {
        "status": "healthy",
        "checked_at": now_iso(),
        "gateway": {"status": "healthy"},
        "services": {"Chirp": {"status": "healthy", "local": True}},
    }


# ---- HTTP server -----------------------------------------------------------

class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):  # quiet
        pass

    def _send(self, status, obj):
        body = json.dumps(obj).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _read_body(self):
        n = int(self.headers.get("Content-Length") or 0)
        return self.rfile.read(n) if n else b""

    def do_GET(self):
        if self.path == "/rpc/_health":
            return self._send(200, health_body())
        if self.path == "/rpc/_introspect":
            return self._send(200, INTROSPECT)
        return self._send(404, {"error": {"message": "not found", "code": "NOT_FOUND"}})

    def do_POST(self):
        path = self.path
        body = self._read_body()
        if path == "/rpc/_health":
            return self._send(200, health_body())
        if path == "/rpc/_introspect":
            return self._send(200, INTROSPECT)
        if path.startswith("/rpc/Chirp/"):
            method = path[len("/rpc/Chirp/"):]
            entry = METHODS.get(method)
            if entry is None:
                return self._send(404, {"error": {"message": f"method {method!r} unknown", "code": "NOT_FOUND"}})
            fn, positional = entry
            try:
                a = parse_args(body, positional)
                data = fn(self.headers, a)
                return self._send(200, {"data": data})
            except RPCError as e:
                return self._send(e.status, {"error": {"message": e.message, "code": e.code}})
            except Exception as e:  # noqa: BLE001 — never 500-crash the pod
                return self._send(500, {"error": {"message": str(e), "code": "INTERNAL"}})
        return self._send(404, {"error": {"message": "not found", "code": "NOT_FOUND"}})


# ---- register + heartbeat --------------------------------------------------

def register_once():
    body = json.dumps({
        "name": SERVICE,
        "address": ADVERTISE,
        "heartbeat_interval_seconds": HEARTBEAT_SECONDS,
        "introspect": True,
    }).encode()
    # Mesh-secret signature over the canonical message (contract §3).
    ts = str(int(time.time()))
    body_hash = hashlib.sha256(body).hexdigest()
    canonical = f"v1\nregister\n{body_hash}\n{ts}\n".encode()
    sig = hmac.new(MESH_SECRET, canonical, hashlib.sha256).hexdigest()
    req = urllib.request.Request(
        GATEWAY + "/rpc/_register", data=body, method="POST",
        headers={
            "Content-Type": "application/json",
            "X-Sov-Register-Sig": sig,
            "X-Sov-Register-Ts": ts,
        })
    with urllib.request.urlopen(req, timeout=5) as resp:
        return resp.status, resp.read()


def heartbeat_loop():
    while True:
        try:
            status, _ = register_once()
            if status != 200:
                print(f"[chirp-py] register status {status}", flush=True)
        except Exception as e:  # noqa: BLE001
            print(f"[chirp-py] register failed: {e}", flush=True)
        time.sleep(HEARTBEAT_SECONDS)


def main():
    port = int(LISTEN.lstrip(":")) if LISTEN.startswith(":") else int(LISTEN.rsplit(":", 1)[1])
    threading.Thread(target=heartbeat_loop, daemon=True).start()
    srv = ThreadingHTTPServer(("", port), Handler)
    print(f"[chirp-py] polyglot Chirp pod on :{port} -> gateway {GATEWAY} (advertise {ADVERTISE})", flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    main()
