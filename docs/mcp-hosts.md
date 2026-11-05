# MCP client configuration

The MCP client starts `homelab-evidence-mcp` as a child process over **stdio**.
The client must give the binary path and configuration file. It must also give
the necessary environment variables.

## Generic JSON configuration

```json
{
  "mcpServers": {
    "homelab-evidence": {
      "command": "/usr/local/bin/homelab-evidence-mcp",
      "args": ["--config", "/home/user/.config/homelab-evidence/config.yaml"],
      "env": {
        "HEALTHCHECKS_API_TOKEN": "readonly-key"
      }
    }
  }
}
```

Some clients use a list rather than a named object:

```json
{
  "name": "homelab-evidence",
  "command": "/usr/local/bin/homelab-evidence-mcp",
  "args": ["--config", "/path/config.yaml"],
  "env": {
    "HEALTHCHECKS_API_TOKEN": "readonly-key"
  }
}
```

Change only the envelope that the client expects. Keep the command, arguments,
and environment the same. These examples are generic. They are not certified for
a specific host application version. Check the host's official configuration
format before you use one.

## Verification

```bash
chmod 600 /path/config.yaml
homelab-evidence-mcp --config /path/config.yaml --validate
homelab-evidence-mcp --version
```

On Windows, use an ACL in place of `chmod 600`. Give access only to the account
that starts the MCP client. The binary does not examine Windows ACLs.

After startup, use standard output only for JSON-RPC. Do not send logs to
standard output. The binary writes logs to standard error.

## Interoperability checks

The project runs a compact check with a client implementation that does not
share the server's Go SDK:

- Client: Python `mcp` package 2.3.0.
- Server: the current source version, stdio transport.
- Method: `scripts/interop_check.py <binary>` on a Linux amd64 CI runner.
- Date: 2026-11-05.
- Result: pass for initialization, version negotiation, tool discovery, all
  seven tool schemas, representative calls, structured and text results,
  invalid arguments, a partial source failure, client cancellation, and clean
  shutdown.

The check starts loopback HTTP fixtures and a temporary configuration. It uses
synthetic values and no model provider. Server diagnostics go to stderr; stdout
stays valid JSON-RPC.

Run it locally after `make build`:

```bash
python3 -m pip install mcp==2.3.0
python3 scripts/interop_check.py ./bin/homelab-evidence-mcp
```

This check covers the protocol and the server contract. It does not certify a
desktop host application. Record a host name and version only after executing
that host against the server.
