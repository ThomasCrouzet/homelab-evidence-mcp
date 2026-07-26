# Exemple d’intégration générique

Cet exemple utilise uniquement des noms, domaines et identifiants fictifs.

## Topologie

| Rôle | Accès |
|---|---|
| Hôte du client MCP | lance `homelab-evidence-mcp` en processus stdio |
| Hôte de supervision | expose Gatus, Loki et Healthchecks |
| Hôtes Docker | exposent un proxy de socket limité en lecture |

## Services pilotes

| `service_id` | Clé Gatus | Conteneur | Sélecteur Loki | Healthchecks |
|---|---|---|---|---|
| `reverse-proxy` | `infra_proxy` | `caddy` | `{container="caddy"}` | tag `proxy` |
| `media` | `media_app` | `media` | `{container="media"}` | tag `media` |
| `git-forge` | `forge_web` | `gitea` | `{container="gitea"}` | facultatif |
| `monitoring` | `monitoring_grafana` | `grafana` | `{container="grafana"}` | nom `monitoring-heartbeat` |

Commencer par quelques services limite le bruit et facilite la vérification des
correspondances.

## Configuration minimale

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

Stocker le fichier réel hors du dépôt avec le mode `0600` sous Unix, ou une ACL
limitée au compte utilisateur sous Windows.

## Vérification progressive

1. Créer uniquement des secrets d’accès en lecture seule.
2. Restreindre le proxy Docker aux routes `GET` nécessaires.
3. Valider la configuration avec `--validate`.
4. Comparer `service_status` aux interfaces des sources.
5. Tester l’expurgation avec un faux secret.
6. Simuler un incident sans interrompre la production.
7. Étendre la couverture seulement après validation des services pilotes.

Le binaire reste un processus enfant stdio ; il n’a pas besoin d’être exposé
comme service réseau permanent.
