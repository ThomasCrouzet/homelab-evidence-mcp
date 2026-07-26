# Compatibilité des API

Seules les formes couvertes par les tests `httptest` sont garanties. D’autres
versions peuvent fonctionner sans être officiellement prises en charge.
Une collection JSON `null` ou une enveloppe privée de son tableau attendu
produit une erreur explicite ; elle ne devient pas une liste vide réussie.

## Gatus

- Route : `GET /api/v1/endpoints/statuses`.
- Corps : tableau d’objets avec `name`, `group`, `key` et `results`.
- Résultat : `success`, `status`, `timestamp`, `errors` et `duration`.
- Le dernier état est choisi par le plus grand horodatage valide.
- Listes vides, résultats vides, champs inconnus et historique désordonné sont testés.

## Docker Engine

- Route : `GET /containers/json?all=true`.
- Champs lus : `Names`, `Image`, `State`, `Status`, quelques `Labels` et `Created`.
- Comparaison de nom insensible à la casse et au `/` initial.
- Une erreur réseau ou un JSON invalide ne devient jamais une liste vide réussie.
- Aucun endpoint inspect, create, start, stop ou exec n’est appelé.
- `observed_at` correspond à la collecte ; `attributes.snapshot` vaut `true`.
- Une fenêtre historique exclut cet instantané courant.

## Loki

- Route : `GET /loki/api/v1/query_range`.
- Paramètres : `query`, `start`, `end`, `limit` et `direction=forward`.
- Réponse : flux contenant des couples `[timestamp_nanosecondes, ligne]`.
- Le type de résultat, lorsqu’il est présent, doit être `streams`.
- Les labels sont filtrés, expurgés et bornés.
- Les timestamps hors fenêtre sont ignorés.
- La limite est réappliquée localement si le serveur la dépasse.
- Le statut 429 produit une erreur explicite.

## Healthchecks

- Route : `GET /api/v3/checks/`.
- Authentification par l’en-tête configuré, `X-Api-Key` par défaut.
- Champs utilisés : `name`, `slug`, `tags`, `status`, `grace`, `last_ping` et `next_ping`.
- URL de ping et UUID ne sont jamais émis.
- Les réponses enveloppées et les tableaux bruts sont acceptés.
- La liste permet d’exposer l’état courant, pas de prouver un incident passé.
- Une fenêtre historique exclut cet état courant.

## Beszel

- Routes essayées : `/api/systems`, `/api/beszel/systems` et variante avec slash final.
- Tableaux et enveloppes `systems`, `items` ou `data` acceptés, y compris vides.
- Identité par `name`, `system`, `host` ou `info.h`.
- Valeurs `cpu` et `mem` facultatives, zéro inclus.
- Instantané uniquement.
- Une fenêtre historique exclut cet instantané courant.

## ntfy

- Route : `GET /{topic}/json?poll=1&since=<unix>`.
- Sujet défini uniquement dans la configuration.
- NDJSON et tableaux JSON acceptés.
- Champs : `id`, `time`, `event`, `message`, `title`, `priority` et `tags`.
- Les événements autres que `message` sont ignorés.
- Une ligne NDJSON invalide ou tronquée produit une erreur de source explicite.
- Les messages sont triés avant l’application de la limite.
