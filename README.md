# LightMail

Client mail **léger, multi-comptes et auto-hébergeable** : un binaire Go unique (interface React embarquée), une base SQLite, une image Docker de ~28 Mo qui tient dans une vingtaine de Mo de RAM au repos.

- IMAP (TLS / STARTTLS obligatoires) et SMTP (465 / 587), synchronisation incrémentale (UID, UIDVALIDITY, CONDSTORE), IMAP IDLE
- **OAuth2** Google et Microsoft (Authorization Code + PKCE, XOAUTH2), comptes IMAP/SMTP classiques (Yahoo, iCloud, Fastmail, Zoho, Orange, Free, La Poste, Proton via Bridge, serveur personnalisé)
- Boîte de réception unifiée, favoris, dossiers, recherche avec opérateurs (`from:`, `has:attachment`, `is:unread`, `after:`…), recherche serveur
- Lecture HTML **assainie et isolée** (iframe sandbox sans script, images distantes bloquées par défaut), pièces jointes à la demande
- Rédaction texte/HTML, brouillons synchronisés, pièces jointes, **annulation d'envoi**, gestion des envois au résultat incertain
- Secrets chiffrés au repos (AES-256-GCM), sessions sécurisées, CSRF, protection anti force brute, journaux sans secrets
- Notifications temps réel (SSE), notifications navigateur, webhook optionnel, métriques Prometheus
- Sauvegarde/restauration vérifiables, rotation de la clé de chiffrement, image non root en lecture seule, amd64 et arm64

> Nom provisoire : l'affichage se change avec `LIGHTMAIL_APP_NAME` (voir [docs/INSTALL.md](docs/INSTALL.md#renommer-lapplication)).

## Démarrage rapide

Prérequis : Docker et Docker Compose v2, un nom de domaine et un reverse proxy HTTPS (Caddy fourni).

```bash
git clone <ce dépôt> lightmail && cd lightmail
cp .env.example .env                 # renseigner LIGHTMAIL_PUBLIC_URL, LIGHTMAIL_ADMIN_PASSWORD…
docker compose build
make secrets                         # ou : docker run --rm lightmail gen-key > secrets/master_key
docker compose up -d
# HTTPS automatique avec le Caddy fourni :
LIGHTMAIL_DOMAIN=mail.example.com docker compose --profile caddy up -d
```

Ouvrez `https://mail.example.com`, connectez-vous avec `admin` / `LIGHTMAIL_ADMIN_PASSWORD`, puis **Ajouter un compte**.
Pour Gmail et Outlook en OAuth2, créez d'abord les applications OAuth : [docs/OAUTH.md](docs/OAUTH.md).

⚠️ **Sauvegardez `secrets/master_key` hors du serveur.** Sans elle, les mots de passe et jetons stockés sont irrécupérables (les emails restent sur vos serveurs de messagerie).

## Documentation

| Sujet | Document |
|---|---|
| Installation, configuration, reverse proxy | [docs/INSTALL.md](docs/INSTALL.md) |
| OAuth2 Google et Microsoft | [docs/OAUTH.md](docs/OAUTH.md) |
| Comptes IMAP/SMTP, mots de passe d'application, Proton Bridge | [docs/CUSTOM_IMAP.md](docs/CUSTOM_IMAP.md) |
| Architecture et choix techniques | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) |
| Sécurité (modèle de menace, secrets, rotation de clé) | [docs/SECURITY.md](docs/SECURITY.md) |
| Sauvegardes, restauration, migration de VPS | [docs/BACKUP.md](docs/BACKUP.md) |
| Mises à jour | [docs/UPGRADE.md](docs/UPGRADE.md) |
| Exploitation et diagnostic | [docs/OPERATIONS.md](docs/OPERATIONS.md) |
| API REST (OpenAPI) | [docs/API.md](docs/API.md) |
| Tests exécutés et mesures de performance | [docs/TESTING.md](docs/TESTING.md) |
| Limitations connues | [docs/LIMITATIONS.md](docs/LIMITATIONS.md) |

## Ressources mesurées

Mesures réelles (conteneur limité à 1 CPU / 512 Mio, détails et méthode dans [docs/TESTING.md](docs/TESTING.md)) :

| Situation | Mémoire (cgroup) | RSS | CPU |
|---|---|---|---|
| Au repos, sans compte | 10 Mio | 20 Mo | 0,05 % |
| Après synchro de 10 000 messages | 23 Mio | 30 Mo | 0,06 % au repos |
| Après synchro de 100 000 messages | 76 Mio (dont cache de pages SQLite) | 38 Mo | 0,04 % au repos |
| 3 comptes × 10 000 messages | 42 Mio | 34 Mo | — |

Synchronisation initiale de 100 000 en-têtes : **17,8 s** ; affichage d'une page de liste : **~2 ms** côté serveur.

## Développement

```bash
make dev-mail        # serveurs IMAPS/SMTPS locaux avec messages générés
make dev             # backend (export LIGHTMAIL_MASTER_KEY=$(go run ./backend/cmd/lightmail gen-key))
cd frontend && npm run dev   # interface sur http://localhost:5173 (proxy vers :8080)
make test            # tests Go + TypeScript
```

### Publier une release

```bash
bash scripts/release.sh patch    # 0.2.1 → 0.2.2 (ou minor, major, 1.4.0, 1.4.0-rc.1)
bash scripts/release.sh          # publie la version actuelle de frontend/package.json
```

Le script vérifie que l'arbre est propre et à jour, commite le changement de version, crée le tag `vX.Y.Z` et le pousse. Le workflow [release.yml](.github/workflows/release.yml) lance alors les tests, construit les binaires Linux (amd64, arm64) et l'image Docker `ghcr.io/cyril59310/lightmail`, puis publie la release GitHub avec les notes générées depuis les commits. Une version avec suffixe (`-rc.1`) est publiée en pré-release, sans le tag `latest`.

Structure : `backend/` (Go : `cmd/lightmail`, `internal/*`, `migrations/`), `frontend/` (React + Vite + TypeScript), `deployments/` (Caddy, nginx, Traefik), `docs/`.

## Licence

MIT (voir [LICENSE](LICENSE)). La licence peut être changée avant toute diffusion publique.
