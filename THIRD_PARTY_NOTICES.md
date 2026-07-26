# Composants tiers

Les dépendances sont déclarées dans `go.mod` et verrouillées par `go.sum`.

## Dépendances directes

| Module | Usage |
|---|---|
| `github.com/modelcontextprotocol/go-sdk` | protocole client et serveur MCP |
| `gopkg.in/yaml.v3` | décodage de la configuration |

Le SDK MCP est distribué sous un ensemble Apache-2.0/MIT et `yaml.v3` sous un
ensemble MIT/Apache. Les dépendances indirectes sont listées dans `go.mod`.

Chaque publication joint `LICENSE.txt` ainsi que `THIRD_PARTY_LICENSES.txt`,
généré depuis le graphe réel des modules incorporés au binaire et contenant
tous les fichiers de licence ou de notification trouvés à la racine de chaque
module.

Le présent projet est distribué sous licence MIT. Voir [LICENSE](LICENSE).
