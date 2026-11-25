#!/usr/bin/env python3
"""Deterministic HTTP forward proxy for local gateway testing.

Accepts absolute-URI proxy requests (the shape Go's net/http.Transport uses
for plain http:// upstreams) and forwards them upstream, logging every hop so
a local run can prove the request actually went through the proxy.

Also speaks SOCKS5 for the socks5/socks5h schemes: it greets the client,
answers with "no authentication required", then relays bytes to the requested
address so the gateway's SOCKS path can be exercised end to end.

The proxy deliberately supports a username/password check so the redaction
path (proxy password must never reach logs or the console) can be verified.
"""
import base64
import os
import socket
import socketserver
import struct
import sys
import threading
from http.server import BaseHTTPRequestHandler
from urllib.parse import urlparse

PROXY_USER = os.environ.get("PROXY_USER", "")
PROXY_PASS = os.environ.get("PROXY_PASS", "")

HITS = []
HITS_LOCK = threading.Lock()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        pass

    def authorized(self):
        if not PROXY_USER:
            return True
        header = self.headers.get("Proxy-Authorization") or ""
        if not header.lower().startswith("basic "):
            return False
        try:
            raw = base64.b64decode(header.split(" ", 1)[1]).decode("utf-8")
        except Exception:
            return False
        user, _, password = raw.partition(":")
        return user == PROXY_USER and password == PROXY_PASS

    def forward(self, method):
        if not self.authorized():
            self.send_response(407)
            self.send_header("Proxy-Authenticate", 'Basic realm="mock-proxy"')
            self.send_header("Content-Length", "0")
            self.end_headers()
            return

        target = self.path
        if not target.startswith("http"):
            self.send_response(400)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return

        parsed = urlparse(target)
        host = parsed.hostname
        port = parsed.port or 80
        path = parsed.path or "/"
        if parsed.query:
            path += "?" + parsed.query

        length = int(self.headers.get("content-length") or 0)
        body = self.rfile.read(length) if length else b""

        try:
            upstream = socket.create_connection((host, port), timeout=15)
        except Exception as exc:  # noqa: BLE001 - surfaced to the client
            payload = f"proxy upstream error: {exc}".encode()
            self.send_response(502)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return

        with HITS_LOCK:
            HITS.append(
                {
                    "method": method,
                    "target": target,
                    "auth": bool(self.headers.get("Proxy-Authorization")),
                }
            )
            print(
                f"proxy: {method} {target} auth={bool(self.headers.get('Proxy-Authorization'))}",
                flush=True,
            )

        forward_headers = []
        for key, value in self.headers.items():
            lowered = key.lower()
            if lowered in ("proxy-authorization", "proxy-connection"):
                continue
            forward_headers.append((key, value))

        request_line = f"{method} {path} HTTP/1.1\r\n"
        request_line += f"Host: {parsed.netloc}\r\n"
        for key, value in forward_headers:
            request_line += f"{key}: {value}\r\n"
        request_line += "Connection: close\r\n\r\n"
        upstream.sendall(request_line.encode("utf-8") + body)

        chunks = []
        while True:
            data = upstream.recv(65536)
            if not data:
                break
            chunks.append(data)
        upstream.close()

        raw = b"".join(chunks)
        if not raw:
            self.send_response(502)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return

        head, _, rest = raw.partition(b"\r\n\r\n")
        status_line, _, header_block = head.partition(b"\r\n")
        status_code = int(status_line.split()[1]) if status_line.split() else 502
        self.send_response(status_code)
        for line in header_block.split(b"\r\n"):
            if not line:
                continue
            key, _, value = line.partition(b":")
            lowered = key.strip().lower()
            if lowered in ("content-length", "transfer-encoding", "connection"):
                continue
            self.send_header(key.strip().decode("utf-8"), value.strip().decode("utf-8"))
        self.send_header("Content-Length", str(len(rest)))
        self.end_headers()
        self.wfile.write(rest)

    def do_GET(self):
        if urlparse(self.path).path == "/health":
            payload = b"ok"
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return
        if self.path.startswith("http"):
            self.forward("GET")
            return
        if self.path.startswith("CONNECT"):
            self.connect_tunnel()
            return
        self.forward("GET")

    def do_POST(self):
        self.forward("POST")

    def do_CONNECT(self):
        self.connect_tunnel()

    def connect_tunnel(self):
        """Tunnel a TLS connection (CONNECT host:port) to the upstream."""
        if not self.authorized():
            self.send_response(407)
            self.send_header("Proxy-Authenticate", 'Basic realm="mock-proxy"')
            self.send_header("Content-Length", "0")
            self.end_headers()
            return

        host, _, port = self.path.partition(":")
        port = int(port or 443)
        try:
            upstream = socket.create_connection((host, port), timeout=15)
        except Exception as exc:  # noqa: BLE001 - surfaced to the client
            self.send_response(502)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return

        with HITS_LOCK:
            HITS.append({"method": "CONNECT", "target": self.path, "auth": True})
        print(f"proxy: CONNECT {self.path} (tunnel)", flush=True)

        self.send_response(200, "Connection established")
        self.end_headers()
        self.wfile.flush()

        def pump(src, dst):
            try:
                while True:
                    data = src.recv(65536)
                    if not data:
                        break
                    dst.sendall(data)
            except Exception:
                pass
            finally:
                try:
                    dst.shutdown(socket.SHUT_WR)
                except Exception:
                    pass

        t = threading.Thread(target=pump, args=(self.connection, upstream), daemon=True)
        t.start()
        pump(upstream, self.connection)
        t.join(timeout=10)
        upstream.close()


class ThreadedHTTPServer(socketserver.ThreadingMixIn, socketserver.TCPServer):
    daemon_threads = True
    allow_reuse_address = True


# ---- SOCKS5 ----


class SocksHandler(socketserver.BaseRequestHandler):
    """Minimal SOCKS5 CONNECT relay with optional username/password auth."""

    def handle(self):
        client = self.request
        try:
            client.settimeout(30)
            greeting = client.recv(2)
            if len(greeting) < 2 or greeting[0] != 0x05:
                return
            nmethods = greeting[1]
            methods = client.recv(nmethods)
            if 0x02 in methods and PROXY_USER:
                # username/password auth (RFC 1929)
                client.sendall(b"\x05\x02")
                head = client.recv(2)
                if len(head) < 2:
                    return
                ulen = head[1]
                user = client.recv(ulen).decode("utf-8")
                plen = client.recv(1)[0]
                password = client.recv(plen).decode("utf-8")
                if user != PROXY_USER or password != PROXY_PASS:
                    client.sendall(b"\x01\x01")
                    return
                client.sendall(b"\x01\x00")
            else:
                client.sendall(b"\x05\x00")

            request = client.recv(4)
            if len(request) < 4 or request[1] != 0x01:
                client.sendall(b"\x05\x07\x00\x01" + b"\x00" * 6)
                return
            atyp = request[3]
            if atyp == 0x01:
                host = socket.inet_ntoa(client.recv(4))
            elif atyp == 0x03:
                length = client.recv(1)[0]
                host = client.recv(length).decode("utf-8")
            else:
                host = socket.inet_ntop(socket.AF_INET6, client.recv(16))
            port = struct.unpack("!H", client.recv(2))[0]

            try:
                upstream = socket.create_connection((host, port), timeout=15)
            except Exception:
                client.sendall(b"\x05\x05\x00\x01" + b"\x00" * 6)
                return

            with HITS_LOCK:
                HITS.append({"method": "CONNECT", "target": f"{host}:{port}", "auth": bool(PROXY_USER)})
            print(f"proxy: CONNECT {host}:{port}", flush=True)

            client.sendall(b"\x05\x00\x00\x01" + socket.inet_aton("0.0.0.0") + struct.pack("!H", 0))

            def pump(src, dst):
                try:
                    while True:
                        data = src.recv(65536)
                        if not data:
                            break
                        dst.sendall(data)
                except Exception:
                    pass
                finally:
                    try:
                        dst.shutdown(socket.SHUT_WR)
                    except Exception:
                        pass

            t = threading.Thread(target=pump, args=(client, upstream), daemon=True)
            t.start()
            pump(upstream, client)
            t.join(timeout=5)
            upstream.close()
        except Exception as exc:  # noqa: BLE001 - a broken client must not kill the server
            print(f"proxy: socks error: {exc}", flush=True)


class ThreadedSocksServer(socketserver.ThreadingTCPServer):
    daemon_threads = True
    allow_reuse_address = True


def main():
    http_port = int(os.environ.get("PROXY_HTTP_PORT", "8888"))
    socks_port = int(os.environ.get("PROXY_SOCKS_PORT", "1080"))

    http_server = ThreadedHTTPServer(("0.0.0.0", http_port), Handler)
    socks_server = ThreadedSocksServer(("0.0.0.0", socks_port), SocksHandler)

    threading.Thread(target=http_server.serve_forever, daemon=True).start()
    threading.Thread(target=socks_server.serve_forever, daemon=True).start()
    print(f"proxy: http on {http_port}, socks5 on {socks_port}", flush=True)
    try:
        threading.Event().wait()
    except KeyboardInterrupt:
        sys.exit(0)


if __name__ == "__main__":
    main()
