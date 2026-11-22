#!/usr/bin/env python3
"""Deterministic mock upstream for local gateway testing.

Serves both protocols on the same port:
  POST /chat/completions   OpenAI
  POST /v1/messages        Anthropic
GET /health returns 200.
"""
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

OPENAI_TEXT = "hello from mock"
ANTHROPIC_TEXT = "hello from mock (anthropic)"


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

        if self.path.endswith("/v1/messages"):
            self.anthropic(model, stream)
        elif self.path.endswith("/chat/completions"):
            self.openai(model, stream)
        else:
            self.send_error(404)

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

    def json_response(self, payload):
        body = json.dumps(payload).encode()
        self.send_response(200)
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
