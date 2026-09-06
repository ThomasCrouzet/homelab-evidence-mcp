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
and environment the same.

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
