# Generic integration example

This example uses only fictional names, domains, and identifiers.

## Topology

| Role | Access |
|---|---|
| MCP client host | runs `homelab-evidence-mcp` as a stdio process |
| Monitoring host | exposes Gatus, Loki, and Healthchecks |
| Docker hosts | expose a read-limited socket proxy |

## Pilot services

| `service_id` | Gatus key | Container | Loki selector | Healthchecks |
|---|---|---|---|---|
| `reverse-proxy` | `infra_proxy` | `caddy` | `{container="caddy"}` | tag `proxy` |
| `media` | `media_app` | `media` | `{container="media"}` | tag `media` |
| `git-forge` | `forge_web` | `gitea` | `{container="gitea"}` | optional |
| `monitoring` | `monitoring_grafana` | `grafana` | `{container="grafana"}` | name `monitoring-heartbeat` |

Starting with a few services limits noise and makes it easier to verify
bindings.

## Minimal configuration

```yaml
version: 1
sources:
  gatus:
    kind: gatus
    base_url: https://gatus.example.internal
  loki:
    kind: loki
    base_url: https://loki.example.internal
  docker-main:
    kind: docker
    base_url: http://docker-proxy.example.internal:2375
  healthchecks:
    kind: healthchecks
    base_url: https://healthchecks.example.internal
    token_env: HEALTHCHECKS_API_TOKEN
services:
  - id: media
    display_name: Media
    sources:
      gatus: { source: gatus, endpoint_key: media_app }
      docker: { source: docker-main, container_name: media }
      loki: { source: loki, selector: '{container="media"}' }
      healthchecks: { source: healthchecks, check_tags: [media] }
```

Store the real file outside the repository with mode `0600` on Unix, or a
user-only ACL on Windows.

## Progressive verification

1. Create only read-only access secrets.
2. Restrict the Docker proxy to the required `GET` routes.
3. Validate the configuration with `--validate`.
4. Compare `service_status` to the source UIs.
5. Test redaction with a fake secret.
6. Simulate an incident without interrupting production.
7. Expand coverage only after pilot services are validated.

The binary remains a stdio child process; it does not need to be exposed as a
permanent network service.
