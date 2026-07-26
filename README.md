# homelab-evidence-mcp

[![CI](https://github.com/ThomasCrouzet/homelab-evidence-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/ThomasCrouzet/homelab-evidence-mcp/actions/workflows/ci.yml)
[![Licence : MIT](https://img.shields.io/badge/Licence-MIT-yellow.svg)](LICENSE)

Serveur MCP local en transport **stdio** qui réunit les preuves d’incident d’un
homelab. Il tient dans un binaire Go statique et ne propose aucune mutation.

## Pourquoi

Lorsqu’un service tombe, les signaux sont dispersés :

- Gatus signale une erreur ;
- Docker montre un conteneur en redémarrage ;
- Loki contient un timeout quelques instants plus tôt ;
- Healthchecks indique une tâche planifiée en échec.

Les outils spécialisés exposent correctement leur propre source, mais ne
partagent ni identité de service, ni format de preuve, ni chronologie commune.
Ce projet fournit cette couche de corrélation sans inventer de causalité.

## Principes

1. **Registre canonique** : un `service_id` explicite relie les identités Gatus,
   Docker, Loki, Healthchecks, Beszel et ntfy.
2. **Modèle de preuve commun** : chaque élément est borné, horodaté, attribué,
   expurgé et marqué lorsqu’il est tronqué.
3. **Corrélation déterministe** : la chronologie présente des faits ; elle ne
   prétend pas établir une cause racine.
4. **Lecture seule structurelle** : seules des requêtes HTTP `GET` vers des
   destinations verrouillées au démarrage sont possibles.

Exemple abrégé :

```text
02:12 Loki  upstream timeout [REDACTED]
02:13 Docker container media is restarting
02:14 Gatus media/app failure status=503
02:15 Healthchecks "media-cron" is down

Timeline is ordered by observed_at; correlation does not establish root cause.
```

Ce serveur n’est ni un tableau de bord, ni un proxy HTTP générique, ni un plan
de contrôle. Il ne redémarre aucun conteneur et ne produit aucune analyse de
cause racine.

## Installation

Prérequis : Go 1.25 ou version ultérieure.

```bash
go install github.com/ThomasCrouzet/homelab-evidence-mcp/cmd/homelab-evidence-mcp@latest
```

Pour construire depuis le dépôt :

```bash
make build
./bin/homelab-evidence-mcp --version
```

## Démarrage rapide

1. Copier `config.example.yaml` vers un emplacement privé.
2. Restreindre le fichier au propriétaire : `chmod 600 /chemin/config.yaml`
   sous Unix, ou une ACL limitée au compte utilisateur sous Windows.
3. Renseigner les URL internes et quelques services pilotes.
4. Exporter les jetons requis, par exemple `HEALTHCHECKS_API_TOKEN`.
5. Valider la configuration avant le branchement au client MCP.

```bash
homelab-evidence-mcp --config /chemin/config.yaml --validate
```

La validation charge les jetons, vérifie les destinations et échoue sur toute
clé YAML inconnue. Les chaînes de requête et fragments sont interdits dans
`base_url` ; l’authentification passe par `token_env` ou `token_file`.

Enregistrement auprès d’un client MCP :

```json
{
  "mcpServers": {
    "homelab-evidence": {
      "command": "/chemin/homelab-evidence-mcp",
      "args": ["--config", "/chemin/config.yaml"],
      "env": {
        "HEALTHCHECKS_API_TOKEN": "readonly-key"
      }
    }
  }
}
```

La sortie standard est réservée au protocole JSON-RPC. Les journaux et
événements d’audit sont écrits sur la sortie d’erreur. Voir
[la configuration des clients MCP](docs/mcp-hosts.md) et
[l’exemple d’intégration](docs/configuration-example.md).

## Sources prises en charge

| Source | Surface utilisée | Garanties principales |
|---|---|---|
| Gatus | `GET /api/v1/endpoints/statuses` | dernier résultat choisi par horodatage |
| Docker Engine | `GET /containers/json?all=true` | champs filtrés, jamais `Config.Env` |
| Loki | `GET /loki/api/v1/query_range` | sélecteur fixé dans la configuration |
| Healthchecks | `GET /api/v3/checks/` | clé en lecture seule, aucune URL de ping |
| Beszel | `GET /api/systems` et variantes compatibles | instantané hôte facultatif |
| ntfy | `GET /{topic}/json?poll=1` | sujet fixé dans la configuration |

Les destinations acceptent uniquement `http` et `https`. Pour Docker, utiliser
un proxy de socket limité en lecture ; le socket Unix direct n’est pas pris en
charge. `HTTP_PROXY` et `HTTPS_PROXY` sont ignorés.

Un préfixe de chemin est autorisé dans `base_url` :
`https://proxy.example/gatus` est correctement combiné avec les routes Gatus.

Le cache de réponses source (`limits.source_cache_ttl`, `15s` par défaut,
`0` pour désactiver) mémorise brièvement les requêtes identiques. Les valeurs
d’authentification ne sont jamais stockées en clair dans ses clés ; une
empreinte HMAC propre au processus distingue les secrets d’accès. Sur un hit,
`observed_at` conserve la date de la collecte originale et `retrieved_at`
indique la relecture courante ; la fraîcheur reflète donc l’âge réel de
l’instantané.

## Outils MCP

| Outil | Usage |
|---|---|
| `evidence_capabilities` | version, sources actives, limites et statistiques |
| `list_services` | services canoniques et couverture |
| `service_status` | instantané Gatus, Docker, Healthchecks et Beszel |
| `incident_context` | chronologie multi-source bornée |
| `search_logs` | recherche Loki sur le sélecteur configuré |
| `failed_crons` | contrôles actuellement `down`, `grace` ou `paused` |
| `get_evidence` | relecture temporaire d’une preuve par identifiant opaque |

Tous les outils sont annotés en lecture seule. Les limites globales bornent les
fenêtres, les corps HTTP, le nombre de preuves et la concurrence.

## Modèle de preuve

Chaque preuve contient notamment sa source, son horodatage d’observation, son
horodatage de collecte, sa sévérité, son état de fraîcheur, son éventuelle
troncature et le nombre d’expurgations appliquées. Les réponses indiquent
également les sources réussies, absentes, ignorées, en erreur ou expirées.

Voir [le modèle de preuve](docs/evidence-model.md) et
[les contrats d’API testés](docs/api-compatibility.md).

## Sécurité

Les garanties et risques résiduels sont détaillés dans
[SECURITY.md](SECURITY.md). Points essentiels :

- destinations verrouillées au démarrage ;
- redirections HTTP refusées ;
- aucune URL ni sélecteur de flux fourni par un appel MCP ;
- contenu des journaux traité comme donnée hostile ;
- expurgation intégrée et règles locales facultatives ;
- configuration et fichiers de jeton limités au mode `0600` sous Unix, ou à
  une ACL utilisateur sous Windows.

## Démonstration locale

```bash
go run ./demo
```

La démonstration lance des serveurs HTTP de test, ouvre une session MCP en
mémoire, vérifie les résultats partiels, l’expurgation et l’absence totale de
requêtes autres que `GET`. Aucun homelab réel n’est requis.

## Développement

```bash
make test          # tests avec détection de courses
make test-quick    # tests rapides
make lint          # formatage, go vet et golangci-lint si disponible
make coverage
make build
```

Le projet construit des binaires Linux, macOS et Windows en CI. Les cibles
principales restent les systèmes Linux et macOS sans interface graphique.

## Licence

MIT. Voir [LICENSE](LICENSE).
