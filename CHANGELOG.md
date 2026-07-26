# Journal des modifications

Les changements notables sont consignés dans ce fichier. Le format suit
[Keep a Changelog](https://keepachangelog.com/fr/1.1.0/) et le versionnement
[Semantic Versioning](https://semver.org/lang/fr/).

## [0.1.0] - 2026-07-26

### Ajouts

- Serveur MCP stdio avec sept outils en lecture seule.
- Registre YAML canonique reliant six familles de sources.
- Adaptateurs Gatus, Docker Engine, Loki, Healthchecks, Beszel et ntfy.
- Client HTTP `GET` uniquement, destinations verrouillées, redirections
  refusées et cache de courte durée facultatif.
- Authentification générique par `token_env`, `token_file` et `token_header`.
- Chronologie déterministe avec résultats partiels explicites.
- Expurgation intégrée et règles locales facultatives.
- Cache temporaire de preuves pour `get_evidence`.
- Budgets de concurrence, de débit et de cardinalité.
- CLI `--version`, `--validate`, `--config` et `--log-level`.
- Fichier d’audit JSONL rotatif facultatif.
- Schéma JSON de configuration et exemples d’intégration.
- Tests de contrat, détection de courses, fuzzing court et démonstration de bout en bout.
- CI, publication de binaires et suivi automatisé des dépendances.

### Sécurité

- Validation YAML stricte : clés inconnues et documents multiples refusés.
- Cardinalité de la configuration bornée pour toutes les collections globales.
- Configuration et fichiers de jeton limités au mode `0600` sous Unix.
- Aucun secret dans les capacités, erreurs ou événements d’audit.
- Valeurs YAML invalides expurgées des erreurs de décodage.
- Expurgation appliquée avant troncature des textes hostiles.
- Identifiants Bearer et Basic expurgés avec leur valeur complète.
- Expurgation et bornes appliquées à tous les attributs textuels issus des
  sources et à leurs identifiants publics.
- Empreintes HMAC propres au processus pour les secrets dans les clés de cache.
- Cache de réponses source plafonné à 128 entrées et 32 Mio.
- Cache de preuves plafonné à 1 000 entrées et textes à 16 Kio.
- Fan-out plafonné à huit requêtes source simultanées.
- Limites de résultats réappliquées côté client.
- Instantanés Docker, Healthchecks et Beszel exclus des fenêtres historiques.
- Chemins absolus, traversées, fragments et chaînes de requête refusés.
- Fichiers d’audit symboliques refusés et permissions resserrées à l’ouverture.
