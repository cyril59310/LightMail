# Exploitation et diagnostic

## Commandes utiles

| Besoin | Commande |
|---|---|
| État et santé du conteneur | `docker compose ps` |
| Journaux (JSON) | `docker compose logs -f lightmail` |
| Journaux lisibles | `LIGHTMAIL_LOG_FORMAT=text` dans `.env`, puis `docker compose up -d` |
| Plus de détails | `LIGHTMAIL_LOG_LEVEL=debug` (aucun secret n'est journalisé, même en debug) |
| Version | `docker compose exec lightmail lightmail version` |
| Santé HTTP | `curl -s http://127.0.0.1:8080/api/v1/health` |
| Consommation | `docker stats lightmail` |
| Métriques Prometheus | `curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8080/api/v1/metrics` (`LIGHTMAIL_METRICS_TOKEN`) |
| État détaillé (connecté) | *Paramètres → État du système* ou `GET /api/v1/status` |
| Sauvegarde / vérification | `make backup`, `make verify-backup FILE=…` |
| Mot de passe admin oublié | `docker compose exec -T lightmail lightmail user set-password admin <<< 'nouveau-mot-de-passe'` |
| Schéma de base | `docker compose exec lightmail lightmail migrate` |

L'image ne contient pas de shell (distroless) : `docker compose exec lightmail sh` est impossible, ce qui est volontaire. Pour inspecter les données, utilisez un conteneur temporaire monté sur le volume :

```bash
docker run --rm -it -v lightmail_lightmail-data:/data:ro alpine sh
```

## Santé et redémarrage

- `HEALTHCHECK` Docker toutes les 30 s (`lightmail healthcheck` → `GET /api/v1/health`, qui vérifie l'accès à la base).
- `restart: unless-stopped` : redémarrage automatique après une panne ou un redémarrage de l'hôte.
- Journaux Docker en rotation : 3 fichiers de 10 Mo (`logging` dans le compose).

## Métriques exposées

| Métrique | Description |
|---|---|
| `lightmail_process_resident_bytes` | Mémoire résidente réelle (VmRSS) |
| `lightmail_go_heap_alloc_bytes`, `lightmail_go_sys_bytes` | Tas Go, mémoire réservée par le runtime |
| `lightmail_cpu_seconds_total` | Temps CPU consommé |
| `lightmail_goroutines` | Goroutines |
| `lightmail_http_requests_total{class}`, `lightmail_http_request_duration_seconds` | Requêtes et latences de l'API |
| `lightmail_sync_runs_total{result}`, `lightmail_sync_duration_seconds` | Synchronisations et durée |
| `lightmail_messages_synced_total` | En-têtes stockés |
| `lightmail_imap_errors_total`, `lightmail_smtp_errors_total`, `lightmail_smtp_sent_total` | Erreurs et envois |
| `lightmail_imap_connections`, `lightmail_imap_idle_connections` | Connexions IMAP ouvertes, dont IDLE |
| `lightmail_messages_cached`, `lightmail_bodies_cached`, `lightmail_blob_bytes`, `lightmail_database_bytes` | Volume du cache |

## Problèmes fréquents

| Symptôme | Piste |
|---|---|
| Le conteneur redémarre en boucle avec `MASTER_KEY does not match this database` | Mauvaise clé montée : restaurez la bonne `secrets/master_key`, ou ajoutez l'ancienne dans `LIGHTMAIL_MASTER_KEY_OLD` |
| `LIGHTMAIL_MASTER_KEY ... is required` | Fichier `secrets/master_key` absent (`make secrets`) |
| `open /data/.lock: permission denied` | Le dossier de données monté depuis l'hôte n'appartient pas à l'utilisateur du conteneur : `sudo chown -R 65532:65532 <dossier>` (voir [INSTALL.md](INSTALL.md#données-dans-un-dossier-de-lhôte-au-lieu-du-volume)) |
| `LIGHTMAIL_MASTER_KEY_FILE: open /run/secrets/lightmail_master_key: permission denied` | La clé n'est pas lisible par l'utilisateur du conteneur (`65532`) : `sudo chown 65532:65532 secrets/master_key && sudo chmod 600 secrets/master_key`, puis `docker compose up -d` |
| Connexion refusée en 403 `bad_origin` | `LIGHTMAIL_PUBLIC_URL` ne correspond pas à l'URL utilisée dans le navigateur (schéma, domaine, port) |
| Cookie de session perdu en HTTP | En HTTPS, le cookie est `Secure`. Pour un test en HTTP, `PUBLIC_URL` doit être en `http://` (ou `LIGHTMAIL_COOKIE_SECURE=false`) |
| Nouveaux messages lents à apparaître | Vérifiez que le reverse proxy ne bufferise pas `/api/v1/events`, et `lightmail_imap_idle_connections` |
| Compte en « Authentification requise » | Mot de passe changé ou jeton OAuth2 révoqué/expiré : *Paramètres → Comptes → Reconnecter* |
| `429 locked` à la connexion | Trop d'échecs : patientez (durée dans `Retry-After`) |
| Erreur OAuth `redirect_uri_mismatch` | L'URI déclarée chez Google/Microsoft doit être exactement `LIGHTMAIL_PUBLIC_URL` + `/api/v1/oauth/<fournisseur>/callback` |
| Gmail : reconnexion demandée chaque semaine | Application OAuth en mode *Test* (jetons de 7 jours), voir [OAUTH.md](OAUTH.md) |

## Entretien automatique

Toutes les 10 minutes : purge des sessions expirées, des états OAuth expirés et des notifications de plus de 90 jours ; suppression des téléversements orphelins de plus de 7 jours ; purge des corps de messages plus anciens que `CACHE_TTL` ; éviction du cache de pièces jointes au-delà de `CACHE_MAX_MB` ou de `CACHE_TTL` ; suppression des fichiers orphelins ; `PRAGMA optimize`.
