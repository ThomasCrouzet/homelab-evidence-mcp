# Generic integration example

This example uses only fictional names, domains, and identifiers.

## Topology

This example has this topology:

- The MCP client host runs `homelab-evidence-mcp` as a stdio process.
- The monitoring host gives access to Gatus, Loki, Healthchecks, Beszel, and
  ntfy.
- Each Docker host gives access through a read-limited socket proxy.

## Pilot services

Use these pilot service connections:

- **`reverse-proxy`**: Use the Gatus endpoint key `infra_proxy`, container `caddy`,
  and Loki selector `{container="caddy"}`. Use the Healthchecks tag `proxy`,
  Beszel system `proxy-host`, and ntfy topic `proxy-alerts`.
- **`media`**: Use the Gatus endpoint key `media_app`, container `media`, and Loki
  selector `{container="media"}`. Use the Healthchecks tag `media`. Beszel and
  ntfy are optional.
- **`git-forge`**: Use the Gatus endpoint key `forge_web` and container
  `gitea`. Use Loki selector `{container="gitea"}`. Healthchecks, Beszel, and
  ntfy are optional.
- **`monitoring`**: Use the Gatus endpoint key `monitoring_grafana` and
  container `grafana`. Use Loki selector `{container="grafana"}` and the
  Healthchecks name `monitoring-heartbeat`. Beszel and ntfy are optional.

Start with some services. This method limits noise and makes the source map
easy to examine.

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
  beszel:
    kind: beszel
    base_url: https://beszel.example.internal
  ntfy:
    kind: ntfy
    base_url: https://ntfy.example.internal
services:
  - id: media
    display_name: Media
    sources:
      gatus: { source: gatus, endpoint_key: media_app }
      docker: { source: docker-main, container_name: media }
      loki: { source: loki, selector: '{container="media"}' }
      healthchecks: { source: healthchecks, check_tags: [media] }
      beszel: { source: beszel, system_name: media-host }
      ntfy: { source: ntfy, topic: media-alerts }
```

On Unix, store the configuration file in a directory that is not in the
repository. Set its mode to `0600`. On Windows, set a user-only ACL. The
binary does not examine Windows ACLs.

## Verification sequence

Use this verification sequence:

1. Make read-only access secrets.
2. Make sure that the Docker proxy has only the necessary `GET` routes.
3. Validate the configuration with `--validate`.
4. Compare `service_status` to the source UIs.
5. Do a redaction test with a test secret.
6. Do an incident test that does not interrupt production.
7. Expand coverage only after you validate the pilot services.

The binary stays a stdio child process. Permanent network exposure is not
necessary.
