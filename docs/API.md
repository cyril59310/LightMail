# API REST

La spécification complète est au format **OpenAPI 3.1** :

- fichier source : [`backend/internal/api/openapi.yaml`](../backend/internal/api/openapi.yaml)
- servie par l'application : `GET /api/v1/openapi.yaml`

## Conventions

- Préfixe versionné : `/api/v1`.
- Authentification par cookie de session `lm_session` obtenu via `POST /api/v1/auth/login`.
- Toute requête modifiante (`POST`, `PUT`, `PATCH`, `DELETE`) doit envoyer `X-CSRF-Token` (renvoyé par `/auth/login` et `/auth/me`) et, pour un corps JSON, `Content-Type: application/json`. Une requête dont l'en-tête `Origin` diffère de `LIGHTMAIL_PUBLIC_URL` est refusée.
- Erreurs : `{"error": {"code": "...", "message": "..."}}`, avec les codes HTTP usuels (400, 401, 403, 404, 409, 413, 415, 422, 429, 502, 504).
- Pagination par curseur opaque : réponse `{"items": [...], "nextCursor": "..."}`, requête suivante avec `?cursor=...`.
- Temps réel : `GET /api/v1/events` (Server-Sent Events) avec les événements `account`, `folders`, `messages`, `newmail`, `outbox` et `notification`.

## Exemple (curl)

```bash
BASE=https://mail.example.com/api/v1
curl -s -c ck.txt -H 'Content-Type: application/json' -H "Origin: https://mail.example.com" \
  -d '{"username":"admin","password":"..."}' $BASE/auth/login        # → {"csrfToken":"..."}
TOKEN=...
curl -s -b ck.txt "$BASE/messages?view=unified&limit=20"
curl -s -b ck.txt "$BASE/search?q=from%3Aalice%20has%3Aattachment"
curl -s -b ck.txt -X POST -H 'Content-Type: application/json' -H "Origin: https://mail.example.com" \
  -H "X-CSRF-Token: $TOKEN" -d '{"ids":[42],"action":"archive"}' $BASE/messages/actions
```

## Principales routes

| Domaine | Routes |
|---|---|
| Session | `POST /auth/login`, `GET /auth/me`, `POST /auth/logout`, `POST /auth/logout-all`, `POST /auth/password`, `GET /auth/sessions` |
| Double authentification | `GET /auth/2fa`, `POST /auth/2fa/setup`, `POST /auth/2fa/enable`, `POST /auth/2fa/disable`, `POST /auth/2fa/recovery` ; `/auth/login` accepte un champ `code` et répond `401 totp_required` quand il manque |
| Fournisseurs | `GET /providers`, `GET /providers/detect?email=` |
| Comptes | `GET/POST /accounts`, `POST /accounts/test`, `GET/PATCH/DELETE /accounts/{id}`, `POST /accounts/{id}/sync`, `/reconnect`, `/default`, `/revoke` |
| OAuth2 | `POST /oauth/{provider}/start`, `GET /oauth/{provider}/callback` |
| Dossiers | `GET/POST /folders`, `PATCH/DELETE /folders/{id}`, `GET /counters` |
| Messages | `GET /messages`, `GET /search`, `GET /messages/{id}`, `/body.html`, `/raw`, `/headers`, `/attachments/{att}`, `POST /messages/actions`, `POST /messages/{id}/edit-draft` |
| Rédaction | `POST /uploads`, `DELETE /uploads/{id}`, `GET/POST /drafts`, `GET/PUT/DELETE /drafts/{id}`, `POST /messages/send`, `GET /contacts` (autocomplétion : contacts enregistrés d'abord, marqués `saved`) |
| Carnet d'adresses (propre à chaque utilisateur) | `GET/POST /address-book` (`{"name", "email", "notes"}`, `409 contact_exists`), `PUT/DELETE /address-book/{id}` (`404` pour le contact d'un autre utilisateur) |
| File d'envoi | `GET /outbox`, `POST /outbox/{id}/cancel`, `POST /outbox/{id}/retry`, `DELETE /outbox/{id}` |
| Divers | `GET /notifications`, `POST /notifications/read`, `GET /events`, `GET/PUT /settings`, `GET /branding/logo`, `GET /metrics` (jeton), `GET /health` |
| Administration (administrateurs, sinon `403`) | `GET /status`, `GET /metrics` (par session), `POST/DELETE /branding/logo`, `GET/POST /users` (création : `{"username", "password", "isAdmin"}`, `409 username_taken`), `PATCH /users/{id}` `{"isAdmin": bool}`, `DELETE /users/{id}` (pas soi-même ; supprime ses comptes mail et données locales) ; `409 last_admin` protège le dernier administrateur |

Par rapport aux routes indicatives du cahier des charges, les actions sur les messages (lu, archive, déplacement, suppression…) sont regroupées dans `POST /messages/actions`, qui accepte jusqu'à 1 000 identifiants et renvoie le résultat message par message. L'envoi est asynchrone (`202` + élément de file d'envoi) ; son résultat arrive par SSE.
