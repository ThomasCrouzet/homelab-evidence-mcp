# MCP client configuration

`homelab-evidence-mcp` runs as a child process over **stdio** transport. The
client must provide the binary path, configuration file, and required
environment variables.

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

Adapt only the envelope expected by the client. The command, arguments, and
environment remain the same.

## Verification

```bash
chmod 600 /path/config.yaml
homelab-evidence-mcp --config /path/config.yaml --validate
homelab-evidence-mcp --version
```

On Windows, replace `chmod 600` with an ACL granting access only to the account
that launches the MCP client.

After startup, standard output is reserved exclusively for JSON-RPC. Do not
redirect logs to stdout; they are written to stderr.
