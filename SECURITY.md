# Politique de sécurité

## Versions prises en charge

| Version | Prise en charge |
|---|---|
| 0.1.x | oui |

## Signaler une vulnérabilité

Ouvrir un avis de sécurité privé sur le dépôt GitHub ou écrire à l’adresse du
mainteneur indiquée sur son profil. Ne pas ouvrir d’issue publique pour une
fuite de secret ou une vulnérabilité non corrigée.

## Modèle de menace

### Actifs protégés

- inventaire des services et signaux de santé ;
- extraits de journaux susceptibles de contenir des secrets d’accès ou des
  données personnelles ;
- jetons d’API Healthchecks ;
- métadonnées Docker filtrées ;
- caches temporaires conservés dans le processus.

### Frontières de confiance

| Entrée ou sortie | Niveau de confiance |
|---|---|
| configuration YAML et environnement au démarrage | entrée de l’opérateur, considérée fiable |
| arguments des outils MCP | non fiables |
| réponses HTTP des sources | non fiables |
| texte des journaux et notifications | donnée hostile |
| stdout | protocole JSON-RPC uniquement |
| stderr et fichier d’audit | données opérationnelles expurgées |

### Lecture seule structurelle

- Aucun outil de démarrage, arrêt, redémarrage, exécution, écriture ou suppression.
- Le client HTTP interne expose uniquement `GET`.
- Les redirections sont refusées.
- Les destinations sont enregistrées depuis la configuration au démarrage.
- Les préfixes de chemin de `base_url` sont préservés.
- Les chemins absolus, traversées de répertoire et changements d’hôte sont refusés.
- Les sélecteurs Loki et sujets ntfy proviennent exclusivement de la configuration.
- Les réponses Docker excluent `Config.Env`, les montages bruts et les labels non autorisés.
- Les réponses Healthchecks excluent UUID, URL de ping, de pause, de mise à jour et de badge.
- La vérification TLS reste active ; aucune option publique ne permet de la désactiver.
- Les budgets limitent le nombre d’appels, la concurrence et les volumes de réponse.
- Le socket Unix Docker direct n’est pas pris en charge.

### SSRF et résolution réseau

Une requête ne peut cibler qu’un nom de destination enregistré au démarrage.
Les schémas sont limités à `http` et `https`, les redirections sont refusées et
les arguments MCP ne peuvent pas fournir d’URL.

Les adresses privées, CGNAT et Tailscale sont autorisées lorsqu’elles figurent
explicitement dans la configuration : elles constituent des destinations
normales pour un homelab.

Le nom d’hôte est verrouillé, mais sa résolution DNS reste effectuée par le
système au moment de la connexion. Un DNS compromis peut donc modifier
l’adresse obtenue. Utiliser des noms internes stables, un DNS maîtrisé ou des
adresses IP fixes pour les destinations sensibles.

### Secrets

- Les jetons proviennent de `token_env` ou d’un `token_file` régulier en mode
  `0600` sous Unix. Sous Windows, appliquer une ACL limitée au compte
  utilisateur ; les bits POSIX n’y sont pas interprétés.
- `token_header` choisit l’en-tête d’authentification ; les en-têtes statiques
  dont le nom évoque un secret d’accès sont refusés.
- Les chaînes de requête, fragments et informations utilisateur sont refusés dans `base_url`.
- Les erreurs et capacités n’affichent ni jeton, ni URL, ni nom d’hôte de
  destination ; elles utilisent le nom logique de la source.
- L’expurgation intégrée couvre notamment mots de passe, bearer tokens,
  cookies, clés courantes, adresses électroniques et en-têtes de clés privées.
- L’expurgation et les bornes s’appliquent aussi aux identifiants et attributs
  textuels issus des sources, pas uniquement aux résumés.
- Les clés du cache HTTP utilisent une empreinte HMAC propre au processus pour
  toutes les valeurs d’en-tête ; aucun secret ni condensat réutilisable n’y
  est stocké en clair.
- Les chemins et chaînes de requête sont eux aussi remplacés par une empreinte
  HMAC dans les clés du cache.
- Le cache HTTP ne conserve aucun en-tête de réponse, notamment `Set-Cookie`.
- La configuration YAML doit elle aussi être limitée au propriétaire.
- Le journal d’audit est créé en mode `0600` sous Unix. Sous Windows,
  l’opérateur doit protéger son chemin avec la même ACL utilisateur.

### Texte hostile

Les lignes Loki et notifications ntfy sont nettoyées, expurgées, bornées et
préfixées par `[UNTRUSTED_LOG_DATA]` lorsqu’elles ressemblent à une instruction.
Le contenu d’origine reste une preuve à traiter comme donnée non fiable.

### Cardinalité et déni de service

- taille maximale de la configuration et des corps HTTP ;
- cardinalité de la configuration bornée pour les sources, services, en-têtes
  et règles d’expurgation ;
- limites sur les lignes, preuves, fenêtres et délais ;
- limites globales par minute et en concurrence ;
- huit requêtes source simultanées au maximum, y compris lors d’un fan-out ;
- cache de réponses source borné à 128 entrées, 32 Mio et une durée de vie ;
- cache de preuves borné à 1 000 entrées et une durée de vie, avec textes
  individuels limités à 16 Kio ;
- réponses partielles explicites lorsqu’une source échoue ;
- limites réappliquées côté client même si une source distante les ignore.

Le cache de réponses source peut être désactivé avec
`limits.source_cache_ttl: 0`. Une durée faible réduit le risque d’afficher un
instantané devenu obsolète. Sur un hit, `observed_at` conserve la collecte
originale et la fraîcheur continue de refléter l’âge réel de la donnée.

### Résultats trompeurs

- L’absence de preuve n’est jamais présentée comme une preuve de bon fonctionnement.
- L’échec d’une source ne supprime pas les résultats des autres.
- Les contradictions sont conservées dans la chronologie.
- Les erreurs réseau ou de décodage ne deviennent jamais une liste vide réussie.
- Gatus choisit le dernier état par horodatage, pas par position.
- L’API de liste Healthchecks ne permet pas d’affirmer un incident passé ou un rétablissement.
- Un état Healthchecks courant est daté à la collecte ; `last_ping` reste un attribut séparé.

### Risques résiduels

- Une configuration compromise peut désigner des services contrôlés par un attaquant.
- Le DNS système reste une dépendance de confiance.
- Un proxy Docker en lecture seule doit être correctement restreint par l’opérateur.
- L’expurgation est une défense en profondeur, pas un système DLP.
- Une dérive d’horloge peut désordonner les événements de plusieurs hôtes.

## Checklist opérateur

1. Utiliser un proxy Docker limité à `GET /containers/json`.
2. Utiliser une clé Healthchecks strictement en lecture seule.
3. Stocker configuration et jetons hors du dépôt en mode `0600` sous Unix,
   ou avec une ACL utilisateur équivalente sous Windows.
4. Exécuter le binaire avec un compte peu privilégié.
5. Préférer HTTPS avec une PKI interne valide.
6. Adapter les règles `redact` aux formats locaux de secrets.
