# Contribuer

Merci de votre intérêt pour le projet.

## Règles

- Le code, les commentaires, les commits et la documentation sont rédigés en français.
- Les noms de variables, fonctions, types et fichiers restent en anglais.
- Ne pas ajouter de ligne d’attribution ou de promotion aux messages de commit.
- Ne pas réduire la couverture, supprimer des tests ni ajouter un `nolint` de convenance.
- Ne pas introduire de mutation : démarrage, arrêt, redémarrage, exécution,
  écriture ou suppression.
- Toute nouvelle dépendance doit être justifiée dans la pull request.

## Installation

```bash
git clone https://github.com/ThomasCrouzet/homelab-evidence-mcp.git
cd homelab-evidence-mcp
go test ./...
go test -race ./...
go run ./demo
```

Go 1.25 ou version ultérieure est requis.

## Style

- Appliquer `gofmt`.
- Produire des erreurs exploitables sans inclure de secrets.
- Préférer de petits paquets sous `internal/`.
- Conserver tous les accès HTTP d’adaptateur derrière `internal/httpx`.

## Tests

Toute modification d’adaptateur doit inclure des tests de contrat avec
`httptest`. Aucun test standard ne doit dépendre d’un homelab réel. Ajouter des
cas hostiles lors d’une modification touchant l’expurgation, le SSRF ou les
journaux.

## Pull requests

1. Limiter chaque pull request à un changement cohérent.
2. Inclure les tests adaptés.
3. Mettre à jour la documentation si les outils, la configuration ou le
   modèle de sécurité changent.
4. Employer des messages de commit courts et impératifs.

Pour une vulnérabilité non corrigée, suivre [SECURITY.md](SECURITY.md) au lieu
d’ouvrir une issue publique.
