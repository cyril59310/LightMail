# Comptes IMAP/SMTP (mot de passe)

Pour les fournisseurs sans OAuth2 intégré, LightMail se connecte en IMAP et SMTP avec un identifiant et un mot de passe. Les deux connexions sont **testées avant l'enregistrement** du compte, et le mot de passe est chiffré (AES-256-GCM) dès réception.

## Sécurité de la connexion

- Seuls **TLS implicite** (IMAPS 993, SMTPS 465) et **STARTTLS obligatoire** (IMAP 143, Submission 587) sont acceptés. Aucune connexion en clair n'est possible, et un serveur qui n'annonce pas STARTTLS est refusé.
- Le certificat et le nom d'hôte du serveur sont **toujours vérifiés** (TLS 1.2 minimum).
- Pour un serveur à certificat auto-signé (Proton Mail Bridge, serveur interne), collez son certificat ou celui de son autorité dans *Certificat CA supplémentaire*. Il s'ajoute aux autorités du système, et la vérification reste active. Aucune option ne permet de désactiver la vérification en production.

## Fournisseurs préconfigurés

| Fournisseur | IMAP | SMTP | Mot de passe à utiliser |
|---|---|---|---|
| Gmail / Workspace | imap.gmail.com:993 TLS | smtp.gmail.com:465 TLS | OAuth2 recommandé, sinon *mot de passe d'application* (validation en 2 étapes requise) |
| Outlook / Microsoft 365 | outlook.office365.com:993 TLS | smtp.office365.com:587 STARTTLS | **OAuth2 uniquement** : Microsoft a supprimé l'authentification basique pour Outlook.com et Exchange Online |
| Yahoo Mail | imap.mail.yahoo.com:993 TLS | smtp.mail.yahoo.com:465 TLS | Mot de passe d'application (sécurité du compte Yahoo) |
| iCloud Mail | imap.mail.me.com:993 TLS | smtp.mail.me.com:587 STARTTLS | Mot de passe pour app (appleid.apple.com), identifiant = adresse iCloud |
| Zoho Mail | imap.zoho.com:993 TLS | smtp.zoho.com:465 TLS | Mot de passe d'application si la 2FA est active. Activez IMAP dans Zoho. Comptes UE : remplacer par `imap.zoho.eu` / `smtp.zoho.eu` |
| Fastmail | imap.fastmail.com:993 TLS | smtp.fastmail.com:465 TLS | Mot de passe d'application obligatoire |
| Orange | imap.orange.fr:993 TLS | smtp.orange.fr:465 TLS | Mot de passe du compte |
| Free | imap.free.fr:993 TLS | smtp.free.fr:465 TLS | Mot de passe du compte (IMAP à activer côté Free) |
| La Poste | imap.laposte.net:993 TLS | smtp.laposte.net:465 TLS | Mot de passe du compte |
| Proton Mail | via Proton Mail Bridge | via Bridge | Mot de passe généré par Bridge (voir ci-dessous) |

Les paramètres restent modifiables dans *Paramètres avancés*. Vérifiez-les dans la documentation du fournisseur si la connexion échoue : ils peuvent évoluer.

**OAuth2 non pris en charge pour Yahoo, iCloud, Fastmail et Zoho.** iCloud n'offre pas OAuth2 pour IMAP. Yahoo et Fastmail réservent l'accès mail OAuth2 aux partenaires approuvés. Zoho propose OAuth2, mais cette intégration n'est pas encore implémentée. L'architecture le permet : il suffit d'une entrée dans `backend/internal/providers/providers.go` et d'identifiants client.

## Serveur personnalisé

Choisissez **Autre (IMAP / SMTP)**. LightMail propose `imap.<domaine>` et `smtp.<domaine>`, ajustables. Renseignez le nom d'utilisateur s'il diffère de l'adresse.

Combinaisons usuelles : IMAP 993/TLS ou 143/STARTTLS ; SMTP 465/TLS ou 587/STARTTLS.

## Proton Mail (via Bridge)

Proton chiffre les messages de bout en bout et n'expose pas d'IMAP public. La seule voie officielle est **Proton Mail Bridge**, réservé aux offres payantes, qui expose un IMAP/SMTP local.

1. Faites tourner Bridge sur une machine joignable par le conteneur LightMail : sur le même hôte, ou dans un conteneur sur le même réseau Docker. Bridge écoute par défaut sur `127.0.0.1:1143` (IMAP) et `127.0.0.1:1025` (SMTP), en STARTTLS avec un certificat auto-signé.
2. Exposez-le au conteneur. Depuis un conteneur, `127.0.0.1` désigne le conteneur lui-même : utilisez le nom du service Bridge sur le réseau Docker, ou l'adresse de l'hôte.
3. Dans Bridge, copiez le mot de passe IMAP/SMTP généré et **exportez le certificat TLS**.
4. Dans LightMail, choisissez *Proton Mail (via Bridge)*, indiquez l'hôte et les ports de Bridge, le mot de passe Bridge, et collez le certificat dans *Certificat CA supplémentaire*.
5. Si Bridge est sur un réseau privé, laissez `LIGHTMAIL_BLOCK_PRIVATE_NETWORKS=false` (valeur par défaut).

## Diagnostic des erreurs courantes

| Message | Cause probable |
|---|---|
| `IMAP authentication failed` | Mot de passe incorrect, mot de passe d'application requis, ou IMAP désactivé chez le fournisseur |
| `the server TLS certificate is not trusted` | Mauvais nom d'hôte, certificat auto-signé (ajoutez la CA), ou interception réseau |
| `connection refused by the server (wrong port?)` | Port ou mode de sécurité incorrect (993/TLS et 143/STARTTLS ne sont pas interchangeables) |
| `STARTTLS ...` | Le serveur n'annonce pas STARTTLS : LightMail refuse de continuer en clair |
| `connection to this address is blocked by the server policy` | Adresse privée alors que `LIGHTMAIL_BLOCK_PRIVATE_NETWORKS=true`, ou adresse link-local / métadonnées cloud (toujours refusées) |
| SMTP `535 5.7.139` (Microsoft) | SMTP AUTH désactivé pour la boîte ou le locataire |

Après une erreur d'authentification, LightMail **suspend les tentatives automatiques** (pour éviter un verrouillage du compte chez le fournisseur) jusqu'à la correction du mot de passe ou un clic sur *Reconnecter*. Les erreurs réseau sont réessayées avec un délai progressif (30 s, 1 min, 2 min… jusqu'à 15 min).
