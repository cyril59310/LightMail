# Tests exécutés et mesures

Toutes les valeurs ci-dessous viennent de commandes réellement exécutées pendant le développement (22 octobre 2026). Rien n'est extrapolé. Ce qui n'a **pas** pu être testé est listé à la fin.

## Environnement

| Élément | Valeur |
|---|---|
| Hôte | Windows 11 Pro, Docker Desktop 29.5.3 (VM Linux, 6 vCPU, 16 Go) |
| Conteneur LightMail | image `lightmail:0.1.0` (28,5 Mo), limites **1 CPU / 512 Mio**, `GOMEMLIMIT=384MiB`, FS en lecture seule |
| Serveur de messagerie de test | go-imap v2 *imapmemserver* + go-smtp, en TLS, dans **un autre conteneur** du même réseau Docker (`cmd/devmail`) |
| Toolchain | Go 1.27.3, Node 25, React 19.3, Vite 8.3 |

Le serveur de test est local : les durées n'incluent **ni la latence Internet ni les limitations de débit** des fournisseurs (Gmail, Microsoft…). Une synchronisation réelle sera plus lente et dépendra surtout du fournisseur.

## 1. Tests automatisés

### Backend Go : 74 tests, tous au vert

`go vet ./...` ne signale rien. `go test ./...` passe, **y compris avec le détecteur de data races** (`go test -race ./...` dans un conteneur `golang:1.27`, aucune course détectée).

| Paquet | Couverture fonctionnelle |
|---|---|
| `config` | Clé obligatoire, secrets `*_FILE`, valeurs invalides refusées, TLS non vérifié interdit en HTTPS |
| `crypto` | Chiffrement/déchiffrement, nonce unique, liaison au contexte, altération détectée, rotation multi-clés |
| `logging` | Masquage des mots de passe, jetons, `Bearer`, `access_token=` et `auth=Bearer` (XOAUTH2) |
| `store` | Migrations idempotentes, compte par défaut, unicité, upsert sans doublon, threads, FTS insensible aux accents, pagination, échappement `LIKE`, isolation inter-utilisateurs, état OAuth à usage unique, file d'envoi (réservation atomique, reprise après crash), autocomplétion, **rotation de clé** |
| `auth` | Argon2id, politique de mot de passe, paramètres absurdes refusés, sessions (expiration d'inactivité, déconnexion des autres), utilisateur inexistant indiscernable, verrouillage anti force brute |
| `netx` | Filtre d'adresses (métadonnées cloud, link-local, privé), filtrage au moment de la connexion, **certificat auto-signé refusé** puis accepté avec CA explicite, délai définitif après timeout |
| `mime` | **22 vecteurs XSS**, blocage des images distantes et `url()` CSS, réécriture `cid:`, liens `noreferrer`, CSS préservé, noms de fichiers (traversée, inversion bidi, contrôle), BODYSTRUCTURE, décodage partiel base64/QP, jeux de caractères, composition (Bcc absent, injection d'en-têtes, References) |
| `imapx` | Certificat non fiable refusé, clair refusé, authentification refusée, rôles des dossiers, FETCH, drapeaux, MOVE avec COPYUID, APPEND, suppression, **XOAUTH2** et jeton révoqué |
| `smtpx` | Envoi, Bcc dans l'enveloppe, refus d'authentification, certificat non fiable, classification **temporaire / définitif / ambigu**, XOAUTH2 |
| `oauth` | Flux complet contre un faux fournisseur OIDC (vérification **PKCE** côté serveur), rejeu du `state` refusé, `state` d'une autre session refusé (CSRF de connexion), erreurs (`access_denied`, `state` falsifié, mauvais code, mauvais fournisseur), contrôle des claims (aud, iss, exp, email_verified, tid), **10 appels concurrents → 1 seul renouvellement**, `invalid_grant` → réauthentification et effacement des jetons |
| `syncer` | Synchro initiale + backfill (120 messages, lots de 50), modifications par un autre client (nouveau message, drapeaux, suppression), corps à la demande, archive/corbeille/suppression définitive, **redémarrage sans doublon**, **changement d'UIDVALIDITY**, **comptes indépendants** (un compte en échec n'affecte pas l'autre), interruption puis reprise, **notification IDLE**, **coupure silencieuse** (détection en un seul délai réseau) |
| `outbox` | Copie unique dans Envoyés, annulation dans le délai, isolation utilisateur, échec temporaire → nouvel essai planifié, échec définitif → notification, **envoi ambigu jamais renvoyé automatiquement** (une seule transmission constatée côté serveur) puis renvoi sur décision |
| `search` | Opérateurs, dates et fuseau horaire, `newer_than`, opérateurs inconnus signalés, **injection FTS5 inerte**, traduction en IMAP SEARCH |
| `blobs` | Déduplication, limite de taille, identifiants malveillants refusés |
| `backup` | Sauvegarde → vérification → perte simulée → restauration, mauvaise clé détectée, **archive piégée (`../`) refusée** |
| `app` (bout en bout HTTP) | **38 routes privées → 401 sans session** ; cookie `HttpOnly`/`SameSite=Lax` ; en-têtes de sécurité ; CSRF (jeton absent, falsifié, Origin étrangère, `Sec-Fetch-Site: cross-site`, Content-Type non JSON) ; validation des paramètres ; déconnexion côté serveur ; verrouillage `429` même avec le bon mot de passe ; callback OAuth falsifié ; parcours mail complet (ajout de compte testé, synchro, lecture, **HTML piégé assaini sous CSP `script-src 'none'`**, images distantes uniquement avec `remote=1`, **pièce jointe HTML servie en `application/octet-stream`**, recherche, téléversement avec traversée de chemin, limite de taille `413`, réponse avec pièce jointe, `In-Reply-To`, Bcc invisible, actions) ; **isolation entre deux utilisateurs** (404 sur messages, corps, pièces jointes, comptes, actions) ; **aucun secret dans les journaux** (mot de passe IMAP, mot de passe admin, jeton CSRF, cookie) |

### Frontend

`tsc --noEmit` passe (TypeScript strict). `vitest` : 4 tests (analyse d'adresses et tentatives d'injection, détection de pièce jointe oubliée, échappement HTML).

### Test d'interface de bout en bout (Chrome headless, playwright-core)

Script exécuté contre le binaire final et les serveurs `devmail` (2 comptes, 400 messages chacun) : connexion (échec puis succès) → ajout de **deux comptes via l'assistant** (test de connexion, certificat CA) → boîte unifiée → recherche `has:attachment` → lecture d'un message piégé (bannière « contenu distant bloqué », **absence de l'URL de pistage dans l'iframe vérifiée**) → raccourcis clavier `j` et `s` → rédaction (`c`), destinataires en pastilles, envoi, toast « Message envoyé » → dossier Envoyés → page État du système → thème sombre → **mobile 390 × 844** (liste, lecteur, retour, tiroir de navigation, **aucun débordement horizontal**). Aucune erreur JavaScript, hormis le 401 attendu de la tentative de connexion volontairement erronée.

## 2. Performances

Méthode : `cmd/devmail` génère N messages (texte, HTML avec image de pistage et pièce jointe 1 fois sur 17, accents). Un script ajoute le ou les comptes par l'API réelle, attend la fin de la synchronisation via `/api/v1/status`, puis mesure les latences des requêtes HTTP (20 à 30 répétitions, p50/p95). `docker stats` est échantillonné chaque seconde. « Mémoire (cgroup) » est la valeur de `docker stats`, qui inclut le cache de pages des fichiers SQLite ; « RSS » est la mémoire résidente du processus (`/proc/self/status`).

### Mémoire et CPU

| Situation | Mémoire (cgroup) | RSS | CPU |
|---|---|---|---|
| Au repos, aucun compte | **9,9 – 10,5 Mio** | 19,9 Mo | 0,05 % |
| Après synchro de 10 000 messages (au repos) | 23,2 Mio | 29,8 Mo | 0,06 % |
| Pic pendant la synchro de 10 000 messages | 23,4 Mio | — | 87 % d'un cœur |
| Après synchro de 3 × 10 000 messages | 41,3 Mio | 33,7 Mo | 0,05 % |
| Après synchro de 100 000 messages (au repos) | 75,5 Mio (cache de pages d'une base de 85,6 Mo inclus) | 37,7 Mo | 0,04 % |
| Pic pendant la synchro de 100 000 messages | 75,7 Mio | — | 103 % (limite 1 CPU) |

Taille de la base : 8,7 Mo pour 10 000 messages, 85,6 Mo pour 100 000 (en-têtes, extraits, index FTS).

### Synchronisation initiale

| Volume | Premiers messages visibles | Synchronisation complète | Débit | CPU consommé |
|---|---|---|---|---|
| 1 compte × 10 000 | 0,54 s | **1,6 s** | 6 387 msg/s | 3,0 s |
| 3 comptes × 10 000 (en parallèle) | 0,08 s | **4,2 s** | 7 226 msg/s | 7,4 s |
| 1 compte × 100 000 | 0,54 s | **17,8 s** | 5 609 msg/s | 28,2 s |

### Latence de l'API (serveur, p50 / p95, en ms)

| Requête | 10 000 messages | 100 000 messages | 3 × 10 000 |
|---|---|---|---|
| Boîte unifiée, 1re page (50) | 2,0 / 3,5 | 1,9 / 3,1 | 1,9 / 3,3 |
| Pages suivantes (défilement × 30) | 1,8 / 2,3 | 1,9 / 2,3 | 1,8 / 2,3 |
| Favoris | 1,9 / 2,3 | 1,9 / 2,4 | 1,8 / 2,7 |
| Filtre non lus | 1,9 / 2,3 | 2,2 / 2,9 | 1,8 / 2,4 |
| Plein texte « facture » | 2,8 / 4,6 | 23,8 / 27,2 | 8,5 / 9,3 |
| `from:alice` | 2,4 / 4,1 | 4,3 / 5,2 | 3,9 / 5,0 |
| `has:attachment is:unread` | 3,8 / 4,9 | 9,5 / 12,2 | 9,9 / 11,6 |
| `subject:contrat after:…` | 2,3 / 2,9 | 3,6 / 4,5 | 3,6 / 3,9 |
| Dossiers + compteurs | 3,5 / 4,1 | 20,2 / 22,4 | 8,1 / 9,1 |
| Ouverture d'un message (corps téléchargé en IMAP) | 1,8 / 6,5 | 2,9 / 116 | 1,7 / 40,9 |
| Ouverture d'un message (corps en cache) | 1,1 / 1,3 | 1,7 / 18,3 | 1,2 / 1,3 |
| Document HTML assaini | 0,9 / 1,1 | 1,2 / 4,5 | 0,9 / 1,2 |

### Optimisations issues des mesures

Le premier banc à 100 000 messages a révélé des problèmes, corrigés puis re-mesurés (mêmes conditions) :

| Mesure | Avant | Après | Cause et correctif |
|---|---|---|---|
| Mémoire au repos | **100,4 Mio** | 10,0 Mio | Argon2id (46 Mio) exécuté deux fois au démarrage, mémoire non rendue. Hash factice paresseux, paramètres OWASP alternatifs (19 Mio, t = 2), `FreeOSMemory` après hachage. |
| Synchro de 100 000 messages | **503 s** (199 msg/s, CPU saturé) | 17,8 s | Recherche de fil par `in_reply_to` sans index, et par `message_id` passant par l'index (compte, date) : un parcours complet par message inséré, donc un coût quadratique. Index ajoutés (migration 0002), `INDEXED BY`, `PRAGMA optimize`. |
| Boîte unifiée, pages suivantes | 249 ms | 1,9 ms | Curseur `(date<? OR …)` non indexable, et tri complet de la boîte. Curseur en *row values*, résolution préalable des dossiers, index de date imposé. |
| Dossiers + compteurs | 103 ms | 20 ms | Compteurs calculés en parcourant tout le compte : passage sur les index partiels `unseen` / `flagged` par dossier. |
| Message en cache | 34 ms | 1,7 ms | Requête de conversation sans index adapté (index `(compte, thread, date)`). |
| Recherche plein texte | 64 – 72 ms | 24 ms | Plan piloté par les correspondances FTS (`CROSS JOIN`). |
| Métrique CPU | erronée | corrigée | `/cpu/classes/total` = CPU disponible, pas consommé. On utilise désormais total − idle. |

## 3. Résilience

| Scénario | Résultat |
|---|---|
| Coupure réseau silencieuse (conteneur IMAP retiré du réseau Docker, sans RST) | Détectée en **60 s** (= `NET_TIMEOUT`). Les 3 comptes passent en « Erreur » avec la cause affichée. |
| Retour du réseau | **Reprise automatique en 33 s**, sans action (1er délai de 30 s + gigue), aucun message perdu ni dupliqué (6 000 / 6 000) |
| Avant correctif | Coupure non détectée après 142 s : crypto/tls signale les timeouts comme temporaires, le parseur relit, et le délai était réarmé à chaque lecture (×4 mesuré). Le timeout est désormais définitif (test `TestSilentNetworkOutage` : détection en 2 s pour un délai de 2 s). |
| Serveur qui change de certificat au redémarrage | **Connexion refusée** (`certificate signed by unknown authority`) : comportement attendu face à un risque d'interception |
| Redémarrage du conteneur LightMail | Arrêt propre en 6 ms (`stopped` journalisé), données conservées, synchronisation reprise sans doublon |
| Arrêt brutal pendant un envoi | Couvert par un test : l'envoi est marqué « incertain » au redémarrage, sans renvoi automatique |

## 4. Déploiement et exploitation (Docker Compose de référence)

Exécuté avec le `docker-compose.yml` du dépôt, `.env` tiré de `.env.example` et secret `secrets/master_key` :

- conteneur **healthy**, utilisateur `65532:65532`, `ReadonlyRootfs=true`, `CapDrop=[ALL]`, limite mémoire 536 870 912 octets ; `GET /` renvoie l'interface avec la CSP complète ; image sans shell (vérifié) ;
- **sauvegarde à chaud** (`lightmail backup`), **vérification** (`checksumsOk`, `integrityOk`, `keyMatches` à true) ;
- **restauration** refusée tant que le serveur tourne (verrou), puis réussie à l'arrêt : le paramètre modifié après la sauvegarde revient à sa valeur sauvegardée ;
- vérification avec une **mauvaise clé** : `keyMatches: false`, code de sortie 1 ; **démarrage avec une mauvaise clé** refusé avec un message explicite ;
- **rotation de clé** (`rotate-key`), puis démarrage avec la seule nouvelle clé : healthy, nouvel identifiant de clé ;
- **migration** d'une base v1 contenant 100 000 messages vers le schéma v2 : appliquée en place ;
- **image ARM64** construite (`docker buildx --platform linux/arm64`) et exécutée (`lightmail version`).

## 5. Non testé (et pourquoi)

| Élément | Raison | Ce qu'il reste à faire |
|---|---|---|
| OAuth2 réel Google et Microsoft | Aucune application OAuth ni compte de test fourni. Le flux est testé contre un faux fournisseur OIDC conforme (PKCE vérifié). | Créer les applications ([OAUTH.md](OAUTH.md)), connecter un compte Gmail et un compte Microsoft 365 / Outlook.com |
| Fournisseurs réels (Gmail, Outlook, Yahoo, iCloud, Fastmail…) | Pas d'identifiants de test | Ajouter un compte de chaque, vérifier dossiers spéciaux, envoi et copie dans Envoyés |
| Chemin CONDSTORE (`CHANGEDSINCE`) | Le serveur de test en mémoire ne gère pas CONDSTORE ; seul le repli (rafraîchissement complet des drapeaux) est testé en intégration | Tester contre Dovecot ou Gmail |
| Navigateurs Firefox et Safari | Seul Chrome (headless) était disponible | Vérification manuelle |
| Audit d'accessibilité outillé | Non réalisé (rôles ARIA, labels et navigation clavier présents) | Audit axe/Lighthouse |
| Charge multi-utilisateur concurrente | Hors périmètre v1 (mono-utilisateur) | — |
| PostgreSQL | Non implémenté (voir [LIMITATIONS.md](LIMITATIONS.md)) | — |

## Reproduire

```bash
cd backend && go vet ./... && go test ./...           # tests Go
make test-race                                        # détecteur de courses (conteneur)
cd frontend && npm run typecheck && npm test          # tests frontend
go run ./backend/cmd/devmail -messages 10000          # serveur de test pour mesurer
```
