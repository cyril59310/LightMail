# Limitations connues (version 0.1.0)

## Non implémenté

- **PostgreSQL** : SQLite uniquement. Tout l'accès aux données passe par `internal/store`, mais les requêtes (FTS5, `INDEXED BY`, `json_each`) sont propres à SQLite. Un portage demanderait un second dialecte (`tsvector` pour la recherche). `LIGHTMAIL_DATABASE_URL=postgres://…` est refusé explicitement au démarrage.
- **OAuth2 Yahoo, Zoho, Fastmail, iCloud** : mot de passe d'application uniquement (voir [CUSTOM_IMAP.md](CUSTOM_IMAP.md) pour les raisons propres à chaque fournisseur).
- **Gestion multi-utilisateur dans l'interface** : le modèle de données isole les utilisateurs (testé), et les administrateurs créent, suppriment et changent le rôle des utilisateurs dans *Paramètres › Utilisateurs*. Pas d'inscription libre ni de réinitialisation de mot de passe par email.
- **WebAuthn / clés de sécurité** pour l'interface (la double authentification se fait par TOTP).
- **Filtres et règles de classement**, étiquettes locales, réponses automatiques.
- **Vue en conversations dans la liste** : les fils sont détectés (Message-ID, In-Reply-To, References) et affichés dans le lecteur, mais la liste reste par message.
- **Synchronisation des contacts et calendriers** (CardDAV/CalDAV). LightMail a un carnet d'adresses local, propre à chaque utilisateur, mais il n'est ni synchronisé avec un serveur ni importable/exportable (vCard).
- **Mode hors ligne / PWA**, notifications Web Push (les notifications navigateur ne fonctionnent qu'onglet ouvert).
- **S/MIME, PGP**.
- **Images collées dans l'éditeur**, glisser-déposer de fichiers dans le compositeur (le bouton *Joindre* fonctionne).
- **Aperçu des pièces jointes** (PDF, images) : téléchargement uniquement, sauf images intégrées `cid:`.
- **Import/export de configuration**. L'export de messages au format EML existe, message par message.
- **Accusés de lecture reçus** : LightMail peut en *demander* (`Disposition-Notification-To`), mais n'en envoie pas automatiquement (choix de confidentialité).

## Comportements à connaître

- **Gmail et ses libellés** : chaque libellé est un dossier IMAP. Un message porteur de plusieurs libellés apparaît dans chacun, et peut donc apparaître en double dans la vue *Favoris* unifiée. « Tous les messages », « Suivis » et « Importants » ne sont pas synchronisés par défaut (réactivables par dossier) pour éviter les doublons. *Archiver* sur Gmail déplace vers « Tous les messages », ce qui retire le libellé Boîte de réception.
- **QRESYNC** n'est pas utilisé (non supporté par go-imap v2) : les suppressions faites par d'autres clients sont détectées par comparaison d'UID quand le nombre de messages ne correspond pas. Le coût reste faible (une liste d'entiers), mais l'opération est plus lourde qu'avec VANISHED.
- **Sans CONDSTORE**, les changements de drapeaux faits ailleurs sont rafraîchis au plus tard toutes les `SYNC_FLAG_REFRESH` (10 min), ou immédiatement pour l'INBOX via IDLE.
- **EXPUNGE sans UIDPLUS** : sur un serveur sans UIDPLUS, la suppression définitive purge aussi les autres messages marqués `\Deleted` du dossier (sémantique IMAP standard, comme Thunderbird).
- **Recherche locale** : elle couvre en-têtes, extraits et corps des messages **déjà ouverts**. La recherche dans le contenu complet passe par *Rechercher sur le serveur* (IMAP SEARCH), dans un dossier à la fois. `has:attachment` n'existe pas en IMAP SEARCH et est appliqué localement sur les résultats serveur.
- **Brouillons sur le serveur** : la copie déposée dans « Brouillons » ne contient pas les destinataires Cci (conservés dans le brouillon local).
- **Copie dans Envoyés** : pour les fournisseurs inconnus, LightMail vérifie par Message-ID qu'une copie n'existe pas déjà avant d'en ajouter une. L'option *Le serveur enregistre lui-même les messages envoyés* désactive la copie.
- **Délai d'annulation d'envoi** : 30 s maximum.
- **Cache** : les corps de messages et pièces jointes téléchargés sont stockés en clair dans le volume, et purgés après `CACHE_TTL` (30 jours) ou au-delà de `CACHE_MAX_MB`. Les en-têtes sont conservés.
- **Sous-chemin** : LightMail doit être servi à la racine d'un domaine ou sous-domaine (`https://mail.example.com`), pas sous `https://example.com/mail`.
- **Windows** : le verrou du répertoire de données n'est actif que sous Linux/Unix (cible de déploiement). Windows n'est utilisé que pour le développement.

## Non vérifié en conditions réelles

Voir [TESTING.md](TESTING.md#5-non-testé-et-pourquoi) : les connexions OAuth2 réelles Google et Microsoft, les fournisseurs du marché et le chemin CONDSTORE n'ont pas pu être testés faute d'identifiants ou de serveur adapté. Le code suit leurs documentations publiques et a été testé contre des serveurs IMAP/SMTP et un fournisseur OIDC conformes.
