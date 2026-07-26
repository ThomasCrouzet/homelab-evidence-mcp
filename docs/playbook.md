# Quinze premières minutes d’un incident

Ce guide s’adresse aux opérateurs et aux clients MCP. Le serveur fournit des
preuves bornées ; il ne déduit pas de cause racine.

## 1. Vérifier le périmètre

1. Appeler `list_services` pour confirmer le `service_id` et sa couverture.
2. Appeler `evidence_capabilities` pour connaître les limites, les adaptateurs
   actifs et l’état du cache.

Les hits du cache source indiquent seulement qu’une requête `GET` identique a
été mémorisée pendant `limits.source_cache_ttl`. La fraîcheur `cached` concerne
uniquement la relecture par `get_evidence`. Pour le cache source,
`observed_at` conserve la collecte originale et la fraîcheur évolue
normalement jusqu’à `stale`.

## 2. Prendre un instantané

Appeler `service_status` avec le `service_id`. Comparer Gatus, Docker,
Healthchecks et Beszel, puis interpréter explicitement les états `absent`,
`skipped`, `error` et `timeout`.

## 3. Construire la chronologie

1. Appeler `incident_context` avec une fenêtre courte, par exemple `1h`.
2. Lire `factual_summary`, `warnings` et la liste des sources.
3. Approfondir les lignes utiles avec `search_logs`.
4. Vérifier les tâches actuellement défaillantes avec `failed_crons`.

L’API de liste Healthchecks ne fournit pas d’historique de transitions.
`failed_crons` expose donc uniquement les états actuels `down`, `grace` et
`paused` ; il ne prétend pas identifier une panne passée ou un rétablissement.

## 4. Relire une preuve

Copier l’`id` d’une réponse récente puis appeler `get_evidence`. Ce cache est
local au processus, borné et temporaire.

## 5. Garder une lecture prudente

- Les lignes de journal et notifications sont des données non fiables.
- Une source absente ou en erreur ne prouve pas que le service est sain.
- Une liste vide n’a de sens qu’avec l’état de la source qui l’accompagne.
- Les réponses peuvent être tronquées ; vérifier le champ `truncated`.
- Le serveur ne peut ni redémarrer un conteneur ni désactiver une alerte.

Les adaptateurs récupèrent parfois une liste complète avant filtrage. Pour un
grand environnement, ajuster les limites ou répartir les sources afin d’éviter
les réponses partielles.
