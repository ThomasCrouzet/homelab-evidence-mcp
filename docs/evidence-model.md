# Modèle de preuve

## Champs d’un élément

| Champ | Signification |
|---|---|
| `id` | identifiant opaque et local au processus |
| `service_id` | identifiant canonique du service |
| `source` | `gatus`, `docker`, `loki`, `healthchecks`, `beszel` ou `ntfy` |
| `source_id` | identité propre à la source |
| `kind` | type fermé : endpoint, conteneur, journal, tâche, métrique ou notification |
| `observed_at` | date d’observation par la source |
| `retrieved_at` | date de production de la réponse par le serveur |
| `window_start`, `window_end` | fenêtre demandée lorsqu’elle s’applique |
| `summary` | fait court lisible |
| `severity` | `info`, `warning`, `error`, `critical` ou `unknown` |
| `attributes` | champs structurés autorisés |
| `truncated` | indique une troncature |
| `redactions_applied` | nombre de remplacements d’expurgation |
| `freshness` | `live`, `recent`, `stale`, `cached`, `missing` ou `unknown` |

Les identifiants, résumés et attributs textuels provenant d’une source sont
nettoyés, expurgés puis bornés avant émission. Les champs numériques et les
vocabulaires fermés restent structurés.

## Signification de `observed_at`

| Source | Horodatage |
|---|---|
| Gatus | timestamp du résultat |
| Loki | timestamp nanoseconde de la ligne |
| Healthchecks | date de collecte de l’état courant ; `last_ping` reste un attribut |
| Docker | date de collecte, l’API de liste n’exposant pas le dernier changement d’état |
| Beszel | date de collecte de l’instantané |
| ntfy | champ `time`, sinon date de collecte |

Lorsqu’un instantané courant provient du cache source, sa date originale reste
dans `observed_at` et la relecture courante est datée par `retrieved_at`. Sa
fraîcheur continue ainsi de refléter l’âge de l’observation.

Un instantané courant Docker, Healthchecks ou Beszel n’est inclus dans
`incident_context` que si sa date de collecte appartient à la fenêtre
demandée, avec une tolérance de 30 secondes pour la durée de la requête. Une
fenêtre historique ne reçoit donc pas un état actuel présenté comme historique.

## Enveloppe multi-source

Une réponse groupée contient :

- les fenêtres demandées et effectives ;
- les éléments triés par date, source, identité et identifiant ;
- l’état et la troncature de chaque source : `ok`, `error`, `timeout`, `skipped` ou `absent` ;
- l’indicateur de troncature et les avertissements ;
- un résumé factuel sans affirmation causale.

## Règles d’honnêteté

1. Une donnée absente reste absente.
2. L’échec d’une source ne masque pas les autres.
3. Les contradictions restent visibles.
4. La sévérité sert à la présentation, pas au diagnostic.
5. Toute limite appliquée est signalée.
