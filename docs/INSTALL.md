# Installation

## Prérequis

- Un VPS Linux (Debian, Ubuntu, Rocky, Alma, Fedora, Arch… : seul Docker est requis), amd64 ou arm64.
- Docker Engine 24+ et le plugin Docker Compose v2.
- Un nom de domaine pointant vers le VPS (indispensable pour HTTPS et pour OAuth2).
- Ressources : 1 vCPU et 256 Mo libres suffisent pour un usage personnel. La limite par défaut du conteneur est de 512 Mio.

## 1. Récupérer le projet et construire l'image

```bash
git clone <dépôt> lightmail && cd lightmail
docker compose build            # ou : make docker
```

L'image est construite en trois étapes (Node pour l'interface, Go pour le binaire, image finale distroless sans shell) et exécutée avec l'utilisateur non privilégié `65532`.

## 2. Configurer

```bash
cp .env.example .env
```

Variables essentielles :

| Variable | Rôle |
|---|---|
| `LIGHTMAIL_PUBLIC_URL` | URL publique exacte (`https://mail.example.com`, sans chemin). Utilisée pour les redirections OAuth2, le cookie `Secure` et le contrôle d'origine CSRF. |
| `LIGHTMAIL_ADMIN_PASSWORD` | Mot de passe du compte `admin` créé au **premier** démarrage (10 caractères minimum). Il n'est jamais réappliqué : vous pouvez le retirer ensuite. |
| `LIGHTMAIL_GOOGLE_CLIENT_ID` / `_SECRET` | Optionnel, OAuth2 Gmail ([OAUTH.md](OAUTH.md)) |
| `LIGHTMAIL_MICROSOFT_CLIENT_ID` / `_SECRET` / `_TENANT` | Optionnel, OAuth2 Microsoft ([OAUTH.md](OAUTH.md)) |

Toutes les variables sont commentées dans [`.env.example`](../.env.example). Chaque secret accepte une variante `*_FILE` (secrets Docker), par exemple `LIGHTMAIL_GOOGLE_CLIENT_SECRET_FILE=/run/secrets/google_secret`.

## 3. Générer la clé de chiffrement

```bash
make secrets
# équivaut à :
mkdir -p secrets && docker run --rm lightmail gen-key > secrets/master_key && chmod 600 secrets/master_key
```

La clé (32 octets aléatoires) chiffre les mots de passe IMAP/SMTP et les jetons OAuth2. Elle est montée en secret Docker (`/run/secrets/lightmail_master_key`) et **n'est jamais incluse dans les sauvegardes**. Conservez-en une copie hors du serveur (gestionnaire de mots de passe). LightMail refuse de démarrer sans clé, ou avec une clé qui ne correspond pas à la base.

## 4. Démarrer

```bash
docker compose up -d
docker compose ps        # STATUS doit passer à "healthy"
docker compose logs -f lightmail
```

Le service écoute uniquement sur `127.0.0.1:8080` : il n'est pas exposé directement sur Internet.

## 5. HTTPS (reverse proxy)

### Option A : Caddy fourni (le plus simple)

```bash
LIGHTMAIL_DOMAIN=mail.example.com docker compose --profile caddy up -d
```

Caddy obtient et renouvelle automatiquement le certificat Let's Encrypt (ports 80 et 443 ouverts requis).

### Option B : reverse proxy existant

Exemples fournis : [nginx](../deployments/nginx.conf), [Traefik](../deployments/traefik-labels.yml), [Caddyfile](../deployments/Caddyfile). Points importants :

- Transmettre `Host` et `X-Forwarded-For`. LightMail ne fait confiance à `X-Forwarded-For` que pour les adresses de `LIGHTMAIL_TRUSTED_PROXIES` (réseaux privés par défaut).
- **Désactiver le buffering** sur `/api/v1/events` (Server-Sent Events), sinon les mises à jour temps réel arrivent en retard.
- Autoriser des corps de requête d'au moins 40 Mo (pièces jointes).
- Ne pas réécrire les en-têtes `Content-Security-Policy` posés par LightMail.

## 6. Premier accès

1. Ouvrez `https://mail.example.com` et connectez-vous avec `admin`.
2. **Ajouter un compte** → choisissez le fournisseur.
3. La synchronisation démarre : les messages récents apparaissent en moins d'une seconde, l'historique arrive ensuite progressivement.

## Gestion des utilisateurs

La version 1 est conçue pour un usage personnel (un utilisateur, plusieurs comptes mail). Le modèle de données isole déjà chaque utilisateur. On peut ajouter des utilisateurs en ligne de commande :

```bash
docker compose exec lightmail lightmail user create alice              # mot de passe lu sur stdin
docker compose exec -T lightmail lightmail user set-password admin <<< 'nouveau-mot-de-passe'
```

`set-password` ferme toutes les sessions de l'utilisateur.

## Renommer l'application

- Nom affiché : `LIGHTMAIL_APP_NAME=MonMail` dans `.env`.
- Nom technique : le binaire, l'image et le préfixe des variables (`LIGHTMAIL_`) sont centralisés dans `backend/internal/config/config.go` (`Prefix`), `Dockerfile`, `docker-compose.yml` et `frontend/index.html`.

## Paramètres de ressources

| Variable | Défaut | Effet |
|---|---|---|
| `LIGHTMAIL_MEM_LIMIT` / `LIGHTMAIL_CPUS` | `512m` / `1.0` | Limites Docker |
| `LIGHTMAIL_GOMEMLIMIT` | `384MiB` | Limite souple du ramasse-miettes Go (à garder sous `MEM_LIMIT`) |
| `LIGHTMAIL_SYNC_INTERVAL` | `5m` | Fréquence de synchronisation (en complément d'IDLE) |
| `LIGHTMAIL_SYNC_IDLE` | `true` | Une connexion IMAP IDLE par compte pour les nouveaux messages instantanés |
| `LIGHTMAIL_IMAP_MAX_CONNS_PER_ACCOUNT` | `2` | Connexions IMAP de travail par compte (+1 pour IDLE) |
| `LIGHTMAIL_SYNC_MAX_MESSAGES_PER_FOLDER` | `0` (tout) | Limite d'en-têtes conservés par dossier |
| `LIGHTMAIL_CACHE_MAX_MB` / `LIGHTMAIL_CACHE_TTL` | `1024` / `720h` | Cache des pièces jointes et des corps de messages |
