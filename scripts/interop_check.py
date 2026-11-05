#!/usr/bin/env python3
"""Independent MCP interoperability check for the stdio server.

Client: mcp (Python SDK) 2.3.0. This is not the Go SDK used by the server.
The script uses loopback fixtures and synthetic values.
"""

import asyncio
import json
import os
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from mcp import Client, StdioServerParameters

CLIENT_NAME = "python-mcp"
CLIENT_VERSION = "2.3.0"


class FixtureHandler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        return

    def do_GET(self):
        if self.path.startswith("/api/v1/endpoints/statuses"):
            body = json.dumps([
                {
                    "name": "api",
                    "key": "api",
                    "results": [{
                        "success": True,
                        "status": 200,
                        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() - 60)),
                    }],
                }
            ]).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path.startswith("/loki/api/v1/query_range"):
            if 'container%3D%22fail%22' in self.path:
                self.send_response(503)
                self.end_headers()
                return
            now = int(time.time() * 1e9)
            body = json.dumps({
                "status": "success",
                "data": {
                    "resultType": "streams",
                    "result": [{
                        "stream": {"container": "api"},
                        "values": [[str(now - 60_000_000_000), "level=info fixture line"]],
                    }],
                },
            }).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(404)
        self.end_headers()


class SlowHandler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        return

    def do_GET(self):
        time.sleep(5)
        body = b"[]"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def start_server(handler):
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, f"http://127.0.0.1:{server.server_address[1]}"


def structured(result):
    value = result.structured_content
    if value is None:
        raise AssertionError("missing structured content")
    return value


def text_object(result):
    blocks = [block.text for block in result.content if getattr(block, "type", "") == "text"]
    if len(blocks) != 1:
        raise AssertionError(f"expected one text block, got {len(blocks)}")
    return json.loads(blocks[0])


async def main(binary):
    fast, fast_url = start_server(FixtureHandler)
    slow, slow_url = start_server(SlowHandler)
    try:
        with tempfile.TemporaryDirectory(prefix="hem interop ") as directory:
            config_path = os.path.join(directory, "config file.yaml")
            with open(config_path, "w", encoding="utf-8") as handle:
                handle.write(f"""version: 1
limits:
  per_source_timeout: 3s
  total_timeout: 10s
  max_log_lines: 10
  max_evidence_items: 20
  max_concurrent_tools: 4
sources:
  gatus:
    kind: gatus
    base_url: {fast_url}
  gatus-slow:
    kind: gatus
    base_url: {slow_url}
  loki:
    kind: loki
    base_url: {fast_url}
services:
  - id: main
    sources:
      gatus: {{ source: gatus, endpoint_key: api }}
      loki: {{ source: loki, selector: '{{container="api"}}' }}
  - id: slow
    sources:
      gatus: {{ source: gatus-slow, endpoint_key: api }}
  - id: failing
    sources:
      gatus: {{ source: gatus, endpoint_key: api }}
      loki: {{ source: loki, selector: '{{container="fail"}}' }}
""")
            os.chmod(config_path, 0o600)
            client = Client(StdioServerParameters(command=binary, args=["--config", config_path]))
            async with client as session:
                tools = await session.list_tools()
                names = {tool.name for tool in tools.tools}
                expected = {
                    "evidence_capabilities", "list_services", "service_status",
                    "incident_context", "search_logs", "failed_crons", "get_evidence",
                }
                if names != expected:
                    raise AssertionError(f"tool catalog={sorted(names)}")
                if not all(tool.output_schema for tool in tools.tools):
                    raise AssertionError("a tool has no output schema")

                capabilities = structured(await session.call_tool("evidence_capabilities", {}))
                if capabilities.get("readonly") is not True:
                    raise AssertionError("capabilities did not declare read-only")
                version = subprocess.run([binary, "--version"], check=True, capture_output=True, text=True).stdout.strip()
                if capabilities.get("version") != version:
                    raise AssertionError(f"version negotiation: {capabilities.get('version')} != {version}")

                services = structured(await session.call_tool("list_services", {}))
                if services.get("total") != 3:
                    raise AssertionError("list_services did not expose three services")

                status = await session.call_tool("service_status", {"service_id": "main"})
                if status.is_error:
                    raise AssertionError("service_status returned an error")
                if text_object(status) != structured(status):
                    raise AssertionError("text and structured content differ")
                if not any(item.get("source") == "gatus" for item in structured(status).get("items", [])):
                    raise AssertionError("service_status returned no gatus evidence")

                logs = structured(await session.call_tool("search_logs", {"service_id": "main", "duration": "1h"}))
                if len(logs.get("items", [])) != 1 or logs.get("source", {}).get("status") != "ok":
                    raise AssertionError("search_logs result is incomplete")
                evidence_id = logs["items"][0]["id"]
                cached = structured(await session.call_tool("get_evidence", {"id": evidence_id}))
                if cached.get("item", {}).get("id") != evidence_id:
                    raise AssertionError("get_evidence did not return the cached item")

                absent = structured(await session.call_tool("failed_crons", {}))
                if absent.get("items") not in ([], None):
                    raise AssertionError("empty failed_crons returned items")

                failing = await session.call_tool("incident_context", {"service_id": "failing"})
                if failing.is_error:
                    raise AssertionError("partial result was an error")
                body = structured(failing)
                if body.get("complete") is not False:
                    raise AssertionError("partial result did not report incomplete coverage")
                loki = next((source for source in body.get("sources", []) if source.get("kind") == "loki"), None)
                if loki is None or loki.get("status") != "error" or loki.get("reason") != "upstream_error":
                    raise AssertionError(f"partial source outcome={loki}")
                if not any(item.get("source") == "gatus" for item in body.get("items", [])):
                    raise AssertionError("partial failure lost gatus evidence")

                invalid = await session.call_tool("service_status", {"service_id": "unknown"})
                if not invalid.is_error:
                    raise AssertionError("invalid arguments did not return an error")

                try:
                    await asyncio.wait_for(session.call_tool("service_status", {"service_id": "slow"}), timeout=0.3)
                    raise AssertionError("slow call did not time out on the client")
                except asyncio.TimeoutError:
                    pass
                after = await session.call_tool("service_status", {"service_id": "main"})
                if after.is_error:
                    raise AssertionError("call after cancellation failed")

            print(json.dumps({
                "client": f"{CLIENT_NAME}/{CLIENT_VERSION}",
                "server": version,
                "transport": "stdio",
                "result": "pass",
            }))
    finally:
        fast.shutdown()
        fast.server_close()
        slow.shutdown()
        slow.server_close()


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("usage: interop_check.py <server-binary>", file=sys.stderr)
        sys.exit(2)
    asyncio.run(main(sys.argv[1]))
