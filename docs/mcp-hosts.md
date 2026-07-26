# Configuration d’un client MCP

`homelab-evidence-mcp` s’exécute comme processus enfant en transport **stdio**.
Le client doit fournir le chemin du binaire, le fichier de configuration et les
variables d’environnement nécessaires.

## Configuration JSON générique

```json
{
  "mcpServers": {
    "homelab-evidence": {
      "command": "/usr/local/bin/homelab-evidence-mcp",
      "args": ["--config", "/home/user/.config/homelab-evidence/config.yaml"],
      "env": {
        "HEALTHCHECKS_API_TOKEN": "readonly-key"
      }
    }
  }
}
```

Certains clients utilisent une liste plutôt qu’un objet nommé :

```json
{
  "name": "homelab-evidence",
  "command": "/usr/local/bin/homelab-evidence-mcp",
  "args": ["--config", "/chemin/config.yaml"],
  "env": {
    "HEALTHCHECKS_API_TOKEN": "readonly-key"
  }
}
```

Adapter uniquement l’enveloppe attendue par le client. La commande, les
arguments et l’environnement restent identiques.

## Vérification

```bash
chmod 600 /chemin/config.yaml
homelab-evidence-mcp --config /chemin/config.yaml --validate
homelab-evidence-mcp --version
```

Sous Windows, remplacer `chmod 600` par une ACL n’accordant l’accès qu’au
compte qui lance le client MCP.

Après démarrage, la sortie standard est exclusivement réservée à JSON-RPC. Ne
pas rediriger les journaux vers stdout ; ils sont écrits sur stderr.
