# OAuth2 : Google et Microsoft

LightMail utilise le flux **Authorization Code avec PKCE (S256)**. L'utilisateur s'authentifie sur la page officielle du fournisseur : LightMail ne voit jamais son mot de passe. Il reçoit un jeton d'accès (≈ 1 h) et un jeton de renouvellement, chiffrés au repos, utilisés en **XOAUTH2** pour IMAP et SMTP.

Vous devez créer **votre propre application OAuth** chez chaque fournisseur : aucun identifiant n'est fourni avec LightMail, et aucun ne doit être commité.

URI de redirection à déclarer (exactement, en remplaçant le domaine) :

| Fournisseur | URI de redirection |
|---|---|
| Google | `https://mail.example.com/api/v1/oauth/google/callback` |
| Microsoft | `https://mail.example.com/api/v1/oauth/microsoft/callback` |

Elles sont dérivées de `LIGHTMAIL_PUBLIC_URL`, jamais de l'en-tête `Host` de la requête.

---

## Google (Gmail, Google Workspace)

### 1. Créer le projet et l'écran de consentement

1. Ouvrez la [Google Cloud Console](https://console.cloudLite.google.com/) et créez un projet (par exemple `lightmail`).
2. **Google Auth Platform** (anciennement « Écran de consentement OAuth ») :
   - *Type d'utilisateur* : **Interne** si vous avez Google Workspace et ne connectez que des comptes de votre organisation (pas de vérification Google nécessaire). Sinon **Externe**.
   - Nom de l'application, e-mail d'assistance, domaine autorisé (`example.com`).
3. **Accès aux données / Scopes** : ajoutez
   - `https://mail.google.com/` (seul scope qui donne accès à IMAP/SMTP ; Google le classe « restreint »),
   - `openid` et `.../auth/userinfo.email` (identifier l'adresse du compte).
4. **Audience** : en mode *Test*, ajoutez vos adresses Gmail comme utilisateurs de test.

### 2. Créer l'identifiant client

**Identifiants → Créer des identifiants → ID client OAuth** :
- Type : **Application Web**
- URI de redirection autorisés : `https://mail.example.com/api/v1/oauth/google/callback`. Pour un test local, `http://localhost:8080/api/v1/oauth/google/callback` est accepté par Google.

Copiez l'ID et le secret dans `.env` :

```ini
LIGHTMAIL_GOOGLE_CLIENT_ID=123456789-xxxx.apps.googleusercontent.com
LIGHTMAIL_GOOGLE_CLIENT_SECRET=GOCSPX-...
# ou, de préférence : LIGHTMAIL_GOOGLE_CLIENT_SECRET_FILE=/run/secrets/google_client_secret
```

Puis `docker compose up -d` pour appliquer la configuration.

### 3. Points d'attention (contraintes Google)

- **Mode Test** (application *Externe* non publiée) : Google fait expirer les jetons de renouvellement **au bout de 7 jours**. Il faut alors reconnecter le compte (bouton *Reconnecter*). Pour un usage durable, passez l'application en production ou utilisez le type *Interne* (Workspace).
- **Application en production non vérifiée** : `https://mail.google.com/` est un scope restreint. Tant que l'application n'est pas vérifiée, Google affiche un écran d'avertissement (« Google n'a pas validé cette application » → *Paramètres avancés* → *Accéder*) et limite le nombre d'utilisateurs. Pour un usage personnel ou familial, c'est acceptable. Une diffusion publique exige la vérification Google, qui inclut une évaluation de sécurité pour les scopes restreints.
- **Google Workspace** : l'administrateur peut bloquer les applications tierces (*Console d'administration → Sécurité → Accès et contrôle des données → Commandes des API → Contrôle des accès des applications*). Déclarez alors l'ID client comme application de confiance. Vérifiez aussi que l'accès IMAP n'est pas désactivé pour l'organisation.
- **Révocation** : *Révoquer l'autorisation* (Paramètres → Comptes) appelle l'endpoint de révocation de Google puis supprime les jetons locaux. L'utilisateur peut aussi révoquer l'accès depuis <https://myaccount.google.com/permissions>.
- **Sans OAuth2** : Gmail accepte aussi un *mot de passe d'application* (validation en deux étapes requise), via le bouton « Utiliser plutôt un mot de passe d'application ».

---

## Microsoft (Outlook.com, Hotmail, Live, Microsoft 365, Exchange Online)

### 1. Enregistrer l'application

1. [Centre d'administration Microsoft Entra](https://entra.microsoft.com/) → **Applications → Inscriptions d'applications → Nouvelle inscription**.
2. *Types de comptes pris en charge* :
   - comptes personnels **et** professionnels : *Comptes dans un annuaire organisationnel et comptes Microsoft personnels* → `LIGHTMAIL_MICROSOFT_TENANT=common` ;
   - uniquement votre organisation : *Locataire unique* → `LIGHTMAIL_MICROSOFT_TENANT=<ID du locataire>`.
3. *URI de redirection* : plateforme **Web**, `https://mail.example.com/api/v1/oauth/microsoft/callback`.

### 2. Secret client

**Certificats et secrets → Nouveau secret client**. Copiez la **Valeur** (pas l'« ID du secret »). Les secrets expirent (24 mois maximum) : notez la date et renouvelez-le à temps, sinon les renouvellements de jetons échouent et les comptes passent en « Reconnexion nécessaire ».

### 3. Permissions

**Autorisations d'API → Ajouter une autorisation → Microsoft Graph → Autorisations déléguées** :

- `IMAP.AccessAsUser.All`
- `SMTP.Send`
- `offline_access`, `openid`, `email`, `profile`

Selon la politique du locataire, un administrateur doit **accorder le consentement administrateur**.

```ini
LIGHTMAIL_MICROSOFT_CLIENT_ID=00000000-0000-0000-0000-000000000000
LIGHTMAIL_MICROSOFT_CLIENT_SECRET=...
LIGHTMAIL_MICROSOFT_TENANT=common
```

LightMail demande les scopes `https://outlook.office.com/IMAP.AccessAsUser.All` et `https://outlook.office.com/SMTP.Send` (audience Exchange Online), comme l'exige Microsoft pour IMAP/SMTP.

### 4. Points d'attention (politiques Microsoft 365)

Tous les comptes Microsoft n'autorisent pas IMAP ou SMTP AUTH. LightMail ne le suppose pas : il teste IMAP **et** SMTP juste après l'autorisation et affiche l'erreur exacte.

- **IMAP désactivé** sur la boîte : l'administrateur peut l'activer (`Set-CASMailbox -Identity user@domaine -ImapEnabled $true`). Si IMAP est refusé, le compte n'est pas créé et l'erreur est affichée.
- **SMTP AUTH désactivé** (cas fréquent, désactivé par défaut sur de nombreux locataires) : `Set-CASMailbox -Identity user@domaine -SmtpClientAuthenticationDisabled $false`, ou réglage au niveau de l'organisation. Le compte est alors créé pour la réception, avec un avertissement sur l'envoi.
- **Accès conditionnel / MFA** : géré par la page de connexion Microsoft. Certaines politiques (appareil géré, emplacement) peuvent bloquer l'application, et l'erreur renvoyée par Microsoft est affichée.
- **Comptes personnels** (outlook.com, hotmail, live) : l'envoi passe par `smtp-mail.outlook.com:587`, choisi automatiquement d'après le locataire indiqué dans le jeton d'identité. Les comptes professionnels utilisent `smtp.office365.com:587`.
- **Révocation** : Microsoft n'offre pas d'endpoint de révocation d'un jeton de renouvellement pour ce type d'application. *Révoquer l'autorisation* supprime les jetons locaux. L'utilisateur retire l'accès depuis <https://account.live.com/consent/Manage> (personnel) ou <https://myapps.microsoft.com> (professionnel).
- Microsoft fait tourner les jetons de renouvellement : LightMail enregistre chaque nouveau jeton reçu.

---

## Cycle de vie des jetons

| Événement | Comportement |
|---|---|
| Jeton d'accès expiré (ou à moins de 2 min de l'expiration) | Renouvellement automatique. Un seul renouvellement à la fois par compte, même avec des requêtes concurrentes. |
| Jeton refusé par IMAP/SMTP alors qu'il semblait valide | Invalidation, un renouvellement, une seule nouvelle tentative |
| `invalid_grant` (jeton révoqué, mot de passe changé, 7 jours en mode Test…) | Jetons effacés, compte en « Reconnexion nécessaire », notification. Les synchronisations automatiques s'arrêtent. |
| Bouton *Reconnecter* | Nouveau flux OAuth avec `login_hint`. LightMail vérifie que la boîte reconnectée est bien la même (sinon erreur `account_mismatch`). |
| Suppression du compte | Révocation (Google) puis effacement des jetons |

## Sécurité du flux

- `state` aléatoire de 256 bits, **à usage unique**, valable 10 minutes, stocké haché et lié à la session du navigateur qui a lancé le flux (protection contre la CSRF de connexion).
- PKCE S256 : le `code_verifier` est stocké chiffré côté serveur, jamais exposé au navigateur.
- Le jeton d'identité est reçu directement de l'endpoint du fournisseur via une connexion TLS vérifiée. `iss`, `aud`, `exp`, `email_verified` (Google) et `tid` (Microsoft) sont contrôlés.
- Les codes, jetons et secrets n'apparaissent jamais dans les journaux. Le résultat du callback est transmis à l'interface dans le fragment d'URL (`#`), qui n'est pas envoyé au serveur.
