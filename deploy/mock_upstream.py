#!/usr/bin/env python3
"""Deterministic mock upstream for local gateway testing.

Serves both protocols on the same port:
  POST /chat/completions   OpenAI
  POST /v1/messages        Anthropic
GET /health returns 200.

The gateway sends the instance's api_key as the Authorization bearer (or
x-api-key on the Anthropic protocol), so the key doubles as a behaviour
switch and the test config can mirror the production topology:

  mock-upstream-key      normal deterministic response
  mock-quota             always 402, standing in for an exhausted plan window
  mock-session-required  demands x-opencode-session, standing in for
                         OpenCode Go's 400 MissingSessionID

Anything else behaves like a normal key.
"""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

OPENAI_TEXT = "hello from mock"
ANTHROPIC_TEXT = "hello from mock (anthropic)"

KEY_NORMAL = "mock-upstream-key"
KEY_QUOTA = "mock-quota"
KEY_SESSION = "mock-session-required"


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.0"

    def log_message(self, fmt, *args):
        print("mock:", fmt % args, flush=True)

    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.end_headers()
            self.wfile.write(b"ok")
        else:
            self.send_error(404)

    def do_POST(self):
        length = int(self.headers.get("content-length") or 0)
        raw = self.rfile.read(length) if length else b"{}"
        try:
            req = json.loads(raw)
        except json.JSONDecodeError:
            req = {}
        stream = bool(req.get("stream"))
        model = req.get("model", "unknown")

        key = self.api_key()
        # Print the session header so a local run can confirm the gateway
        # derived (or passed through) a stable value.
        session = (self.headers.get("x-opencode-session") or "").strip()
        print(
            f"mock: model={model} key={key} session={session[:16] or '(none)'}",
            flush=True,
        )
        if key == KEY_QUOTA:
            # MiniMax reports an exhausted plan window as 402 Payment Required.
            return self.json_response(
                {
                    "error": {
                        "type": "insufficient_balance_error",
                        "message": "当前已达到 Token Plan 用量上限",
                        "http_code": "402",
                    }
                },
                status=402,
            )
        if key == KEY_SESSION and not self.headers.get("x-opencode-session"):
            # OpenCode Go refuses sessionless requests before they reach a model.
            return self.json_response(
                {
                    "type": "error",
                    "error": {
                        "type": "MissingSessionID",
                        "message": "Request is missing x-opencode-session",
                    },
                },
                status=400,
            )

        if self.path.endswith("/v1/messages"):
            self.anthropic(model, stream)
        elif self.path.endswith("/chat/completions"):
            self.openai(model, stream)
        else:
            self.send_error(404)

    def api_key(self):
        """Return the key the gateway sent, whichever header carried it."""
        auth = (self.headers.get("authorization") or "").strip()
        if auth.lower().startswith("bearer "):
            return auth[7:].strip()
        return (self.headers.get("x-api-key") or "").strip()

    # ---- OpenAI ----

    def openai(self, model, stream):
        if not stream:
            payload = {
                "id": "chatcmpl-mock",
                "object": "chat.completion",
                "model": model,
                "choices": [{
                    "index": 0,
                    "message": {"role": "assistant", "content": OPENAI_TEXT},
                    "finish_reason": "stop",
                }],
                "usage": {"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10},
            }
            return self.json_response(payload)

        self.start_sse()
        events = [
            {"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": model,
             "choices": [{"index": 0, "delta": {"role": "assistant", "content": ""}}]},
            {"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": model,
             "choices": [{"index": 0, "delta": {"content": "hello "}}]},
            {"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": model,
             "choices": [{"index": 0, "delta": {"content": "from mock"}}]},
            {"id": "chatcmpl-mock", "object": "chat.completion.chunk", "model": model,
             "choices": [], "usage": {"prompt_tokens": 7, "completion_tokens": 3}},
        ]
        for event in events:
            self.write_sse("", event)
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()

    # ---- Anthropic ----

    def anthropic(self, model, stream):
        if not stream:
            payload = {
                "id": "msg_mock",
                "type": "message",
                "role": "assistant",
                "model": model,
                "content": [{"type": "text", "text": ANTHROPIC_TEXT}],
                "stop_reason": "end_turn",
                "stop_sequence": None,
                "usage": {"input_tokens": 9, "output_tokens": 4},
            }
            return self.json_response(payload)

        self.start_sse()
        self.write_sse("message_start", {
            "type": "message_start",
            "message": {"id": "msg_mock", "type": "message", "role": "assistant",
                        "model": model, "content": [],
                        "usage": {"input_tokens": 9, "output_tokens": 0}},
        })
        self.write_sse("content_block_start", {
            "type": "content_block_start", "index": 0,
            "content_block": {"type": "text", "text": ""},
        })
        for delta in ("hello ", "from mock (anthropic)"):
            self.write_sse("content_block_delta", {
                "type": "content_block_delta", "index": 0,
                "delta": {"type": "text_delta", "text": delta},
            })
        self.write_sse("content_block_stop", {"type": "content_block_stop", "index": 0})
        self.write_sse("message_delta", {
            "type": "message_delta",
            "delta": {"stop_reason": "end_turn", "stop_sequence": None},
            "usage": {"output_tokens": 4},
        })
        self.write_sse("message_stop", {"type": "message_stop"})

    # ---- helpers ----

    def json_response(self, payload, status=200):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def start_sse(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()

    def write_sse(self, event, data):
        if event:
            self.wfile.write(f"event: {event}\n".encode())
        self.wfile.write(f"data: {json.dumps(data)}\n\n".encode())
        self.wfile.flush()


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8000), Handler).serve_forever()
