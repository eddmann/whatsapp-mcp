#!/usr/bin/env python3
"""stdio <-> Streamable HTTP bridge for a shared whatsapp-mcp server.

MCP clients that only launch stdio servers (Claude Desktop, Cowork, Claude Code)
run this script; it forwards each JSON-RPC message to one long-running
whatsapp-mcp instance over HTTP, so all clients share a single WhatsApp session.

Stdlib only, Python 3.9+. Configuration (env):
  WHATSAPP_MCP_URL         default http://127.0.0.1:8787/mcp
  WHATSAPP_MCP_TOKEN_FILE  default ~/.whatsapp-mcp/token  (file holding the bearer token)

If the server restarts, its session is gone (HTTP 404); the bridge replays the
client's original initialize handshake once and retries the request.
"""
import json
import os
import sys
import threading
import urllib.error
import urllib.request

URL = os.environ.get("WHATSAPP_MCP_URL", "http://127.0.0.1:8787/mcp")
TOKEN_FILE = os.path.expanduser(os.environ.get("WHATSAPP_MCP_TOKEN_FILE", "~/.whatsapp-mcp/token"))
TIMEOUT = 300  # seconds; media downloads/sends can be slow

out_lock = threading.Lock()
session_lock = threading.Lock()
state = {"session": None, "protocol": None, "init_params": None}


def log(*args):
    print("[mcp-http-bridge]", *args, file=sys.stderr, flush=True)


def emit(msg):
    line = json.dumps(msg, separators=(",", ":"))
    with out_lock:
        sys.stdout.write(line + "\n")
        sys.stdout.flush()


def read_token():
    with open(TOKEN_FILE, encoding="utf-8") as f:
        return f.read().strip()


TOKEN = read_token()


class SessionExpired(Exception):
    pass


def post(msg, session):
    body = json.dumps(msg).encode()
    req = urllib.request.Request(URL, data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "application/json, text/event-stream")
    req.add_header("Authorization", "Bearer " + TOKEN)
    if session:
        req.add_header("Mcp-Session-Id", session)
    if state["protocol"]:
        req.add_header("MCP-Protocol-Version", state["protocol"])
    try:
        resp = urllib.request.urlopen(req, timeout=TIMEOUT)
    except urllib.error.HTTPError as e:
        if e.code == 404 and session:
            raise SessionExpired()
        raise
    sid = resp.headers.get("Mcp-Session-Id")
    ctype = resp.headers.get("Content-Type", "")
    messages = []
    if resp.status == 202:
        pass
    elif ctype.startswith("text/event-stream"):
        data = []
        for raw in resp:
            line = raw.decode("utf-8").rstrip("\r\n")
            if line.startswith("data:"):
                data.append(line[5:].lstrip())
            elif line == "" and data:
                messages.append(json.loads("\n".join(data)))
                data = []
        if data:
            messages.append(json.loads("\n".join(data)))
    else:
        payload = resp.read()
        if payload.strip():
            parsed = json.loads(payload)
            messages.extend(parsed if isinstance(parsed, list) else [parsed])
    resp.close()
    return sid, messages


def reinitialize(stale):
    """Open a fresh server session by replaying the client's handshake."""
    with session_lock:
        if state["session"] != stale:  # another thread already did it
            return
        log("server session expired; re-initializing")
        sid, _ = post({"jsonrpc": "2.0", "id": "bridge-reinit", "method": "initialize",
                       "params": state["init_params"]}, None)
        state["session"] = sid
        post({"jsonrpc": "2.0", "method": "notifications/initialized"}, sid)


def forward(msg):
    for attempt in (1, 2):
        session = state["session"]
        try:
            sid, replies = post(msg, session)
            if msg.get("method") == "initialize":
                state["session"] = sid
                state["init_params"] = msg.get("params")
                for r in replies:
                    pv = (r.get("result") or {}).get("protocolVersion")
                    if pv:
                        state["protocol"] = pv
            for r in replies:
                emit(r)
            return
        except SessionExpired:
            if attempt == 1 and state["init_params"] is not None:
                try:
                    reinitialize(session)
                    continue
                except Exception as e:  # fall through to error reply
                    err = e
            else:
                err = Exception("server session expired")
        except Exception as e:
            err = e
        break
    log("request failed:", msg.get("method"), repr(err))
    if "id" in msg and "method" in msg:
        emit({"jsonrpc": "2.0", "id": msg["id"],
              "error": {"code": -32000, "message": "whatsapp-mcp bridge: " + str(err)}})


def close_session():
    if not state["session"]:
        return
    req = urllib.request.Request(URL, method="DELETE")
    req.add_header("Authorization", "Bearer " + TOKEN)
    req.add_header("Mcp-Session-Id", state["session"])
    try:
        urllib.request.urlopen(req, timeout=5).close()
    except Exception:
        pass


def main():
    threads = []
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except json.JSONDecodeError:
            log("ignoring non-JSON input")
            continue
        if isinstance(msg, dict) and msg.get("method") == "initialize":
            forward(msg)  # must finish first: it yields the session id
            continue
        t = threading.Thread(target=forward, args=(msg,), daemon=True)
        t.start()
        threads.append(t)
        threads = [t for t in threads if t.is_alive()]
    for t in threads:
        t.join(timeout=10)
    close_session()


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
