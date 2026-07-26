# Feuille de route

Ce document décrit des orientations, pas des engagements ni des dates.

## Version 0.1

- six familles de sources en lecture seule ;
- registre de services canonique ;
- sept outils MCP en transport stdio ;
- modèle de preuve et chronologie déterministes ;
- validation stricte, expurgation, caches et budgets ;
- publication de binaires multi-plateformes.

## Évolutions possibles

- séries temporelles Beszel lorsqu’une API historique stable sera disponible ;
- autres systèmes de notification compatibles avec le même modèle de preuve ;
- paquets Homebrew ou Nix si une demande réelle apparaît ;
- traces OpenTelemetry facultatives limitées aux durées, sans contenu métier.

## Non-objectifs permanents

- modifier Docker, les hôtes, le DNS ou les notifications ;
- devenir un proxy de requêtes d’observabilité généraliste ;
- produire une analyse de cause racine ;
- distribuer des secrets d’accès ou une topologie privée ;
- accepter le socket Unix Docker direct ;
- exposer une option désactivant la vérification TLS.
