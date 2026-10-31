# Sécurité

## Modèle de menace (résumé)

| Menace | Mesures |
|---|---|
| Email malveillant (XSS, pistage, phishing technique) | Assainissement bluemonday + réécriture des images, CSP `script-src 'none'` dédiée, iframe `sandbox` sans `allow-scripts`, images distantes bloquées par défaut, pièces jointes jamais rendues dans l'origine |
| Vol de session / CSRF | Cookie `HttpOnly`, `SameSite=Lax`, `Secure` en HTTPS ; jeton CSRF par session (`X-CSRF-Token`) ; contrôle `Origin` et `Sec-Fetch-Site` ; corps JSON obligatoire |
| Force brute sur la connexion | Argon2id, verrouillage progressif par IP et par identifiant, réponse identique pour un utilisateur inexistant |
| Fuite de secrets (base, sauvegarde, journaux) | Chiffrement AES-256-GCM au repos lié au contexte, clé hors base et hors sauvegarde, masquage dans les journaux, aucune donnée sensible dans l'interface |
| Interception réseau vers les serveurs mail | TLS obligatoire (pas de clair), vérification du certificat et du nom d'hôte, TLS ≥ 1.2, CA additionnelle explicite au lieu d'un contournement |
| SSRF via les paramètres de compte | Filtrage de l'adresse IP réelle **au moment de la connexion** (résiste au DNS rebinding) : link-local et métadonnées cloud toujours refusées, réseaux privés refusables (`BLOCK_PRIVATE_NETWORKS`). Le serveur ne télécharge jamais d'URL issue d'un email. |
| Compromission du conteneur | Image distroless sans shell, utilisateur 65532, système de fichiers en lecture seule, `cap_drop: ALL`, `no-new-privileges`, aucun socket Docker monté |
| Fichiers malveillants / traversée de chemin | Fichiers stockés sous leur SHA-256 (le nom fourni n'atteint jamais le système de fichiers), noms assainis (séparateurs, caractères de contrôle, inversions bidirectionnelles), archives de restauration validées entrée par entrée |

## Contenu des emails

Les emails sont traités comme des données non fiables :

1. **Serveur** : `bluemonday` (politique UGC étendue aux attributs de présentation) retire scripts, gestionnaires d'événements, formulaires, `iframe`/`object`/`embed`, `svg`, `math`, `meta`, `base`, `link` et URL `javascript:`/`vbscript:`/`data:text/html`. Seules les images `data:` matricielles (png, jpeg, gif, webp, bmp) sont conservées. Une seconde passe réécrit `cid:` vers l'endpoint local et retire les images `http(s)` tant que l'utilisateur ne les a pas autorisées. Elle compte aussi les contenus distants bloqués, y compris les `url()` CSS.
2. **Endpoint `/api/v1/messages/{id}/body.html`** : en-tête CSP `default-src 'none'; script-src 'none'; img-src 'self' data:` (plus `https:` seulement avec `?remote=1`), `form-action 'none'`, `base-uri 'none'`, `frame-ancestors 'self'`, directive `sandbox`.
3. **Navigateur** : `<iframe sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox">`. Sans `allow-scripts`, aucun script ne peut s'exécuter, même en cas de faille de l'assainisseur. Liens en `target=_blank` avec `rel="noreferrer"`, référent supprimé (`Referrer-Policy: no-referrer`).
4. **Pièces jointes** : servies en `application/octet-stream` + `Content-Disposition: attachment`, avec `X-Content-Type-Options: nosniff` et CSP `sandbox`. Seules les images matricielles peuvent s'afficher en ligne (images `cid:`).

Les tests couvrent 22 vecteurs XSS connus, l'endpoint HTML de bout en bout et une pièce jointe HTML piégée (`internal/mime/mime_test.go`, `internal/app/e2e_test.go`).

## Authentification à l'interface

- Mots de passe : Argon2id (m = 19 Mio, t = 2, p = 1, paramètres alternatifs recommandés par OWASP), 10 caractères minimum. Deux hachages concurrents au plus, mémoire rendue au système après chaque hachage.
- Sessions : jeton aléatoire de 256 bits dans le cookie, seul son SHA-256 est stocké. Expiration après 7 jours d'inactivité, 30 jours maximum (configurables). Changer de mot de passe ferme les autres sessions. *Déconnecter toutes les sessions* est disponible.
- Anti force brute : par IP, 10 échecs puis verrouillage doublant de 1 min à 1 h ; par identifiant, 5 échecs puis 30 s à 15 min. L'adresse client vient de `X-Forwarded-For` seulement si le proxy figure dans `TRUSTED_PROXIES`.
- Aucune page de création de compte non authentifiée : le premier administrateur vient de `LIGHTMAIL_ADMIN_PASSWORD` ou de la ligne de commande.
- Toutes les routes `/api/v1` hors `health`, `auth/login`, `auth/me` et `openapi.yaml` exigent une session (test automatique sur 38 routes). Chaque ressource est filtrée par utilisateur (test d'isolation inter-utilisateurs).

## En-têtes HTTP

`Content-Security-Policy` stricte (`script-src 'self'`, `frame-ancestors 'none'`, `object-src 'none'`…), `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy` et `Cross-Origin-Resource-Policy: same-origin`, `Permissions-Policy` restrictive, `Strict-Transport-Security` si l'URL publique est en HTTPS, `Cache-Control: no-store` sur l'API.

## Secrets

| Secret | Stockage |
|---|---|
| Clé maître | Secret Docker (`secrets/master_key`, montée en lecture seule), jamais en base ni dans les sauvegardes |
| Mots de passe IMAP/SMTP | `accounts.password_enc` : AES-256-GCM, contexte `account:<id>:password` |
| Jetons OAuth2 | `accounts.oauth_access_enc` / `oauth_refresh_enc` : AES-256-GCM, contextes distincts |
| `code_verifier` PKCE | `oauth_states.verifier_enc`, chiffré, à usage unique, 10 min |
| Secrets clients OAuth2 | Variables d'environnement ou fichiers `*_FILE`, séparés des jetons utilisateurs |
| Mot de passe de l'interface | Hachage Argon2id |

Format des valeurs chiffrées : `v1.<id de clé>.<base64url(nonce‖chiffré)>`. Le **contexte** (table, ligne, champ) est passé en données authentifiées : une valeur copiée vers un autre compte ou un autre champ ne se déchiffre pas. Au démarrage, un marqueur chiffré vérifie que la clé configurée correspond à la base ; sinon le démarrage est refusé avec un message explicite.

**Journaux** : les attributs dont le nom évoque un secret (`password`, `token`, `secret`, `code`, `cookie`…) sont remplacés par `[REDACTED]`, et les motifs `Bearer …`, `access_token=…` et autres sont masqués dans les messages d'erreur. Les URL sont journalisées sans leur chaîne de requête (les codes OAuth n'y apparaissent donc jamais). Un test vérifie, sur un parcours complet, l'absence du mot de passe IMAP, du mot de passe admin, du jeton CSRF et du cookie de session dans les journaux.

## Rotation de la clé maître

```bash
NEW=$(docker run --rm lightmail gen-key)
OLD=$(cat secrets/master_key)
docker compose stop lightmail
docker compose run --rm --no-deps -e LIGHTMAIL_MASTER_KEY_FILE= \
  -e LIGHTMAIL_MASTER_KEY="$NEW" -e LIGHTMAIL_MASTER_KEY_OLD="$OLD" lightmail rotate-key
echo "$NEW" > secrets/master_key
docker compose up -d
```

`rotate-key` rechiffre tous les secrets avec la nouvelle clé dans une transaction unique et refuse de s'exécuter si le serveur tourne (verrou du répertoire de données). Les sauvegardes antérieures restent liées à l'**ancienne** clé : conservez-la tant que vous gardez ces sauvegardes.

## Recommandations d'exploitation

- Exposer LightMail uniquement derrière HTTPS (le port 8080 est lié à `127.0.0.1`).
- Activer `LIGHTMAIL_BLOCK_PRIVATE_NETWORKS=true` si plusieurs personnes utilisent l'instance et qu'aucun serveur mail local (Bridge, LAN) n'est nécessaire.
- Mettre à jour l'image régulièrement ([UPGRADE.md](UPGRADE.md)).
- Ne jamais commiter `.env` ni `secrets/` (déjà dans `.gitignore`).
- Chiffrer les sauvegardes hors site (elles contiennent les métadonnées et les corps de messages en cache).

## Limites de sécurité connues

- Le contenu en cache (en-têtes, corps téléchargés, pièces jointes) est stocké **en clair** dans le volume de données, comme dans un client lourd. Protégez le disque (chiffrement du volume ou du VPS) si nécessaire.
- Les feuilles de style des emails restent actives dans l'iframe isolée, ce qui n'a pas d'impact hors de l'iframe.
- Pas de 2FA sur l'interface en v1 : utilisez un mot de passe fort, et éventuellement une authentification supplémentaire au niveau du reverse proxy.
