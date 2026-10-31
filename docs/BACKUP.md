# Sauvegardes et restauration

## Ce qui est sauvegardé

| Élément | Inclus | Remarque |
|---|---|---|
| Base SQLite (utilisateurs, comptes, paramètres, dossiers, en-têtes, corps en cache, brouillons, file d'envoi, notifications) | Oui | Instantané cohérent via `VACUUM INTO`, possible pendant que LightMail tourne |
| Pièces jointes des brouillons et messages en attente d'envoi | Oui | Indispensables à la reprise |
| Cache des pièces jointes reçues | Option `-with-cache` | Re-téléchargeable depuis les serveurs |
| **Clé maître** | **Non, volontairement** | Sans elle, mots de passe et jetons OAuth2 de la sauvegarde sont inutilisables |

Les emails restent sur les serveurs de vos fournisseurs. Après une reconstruction complète, LightMail les resynchronise : la sauvegarde évite seulement de reconfigurer les comptes et de tout retélécharger.

Format : archive `tar.gz` contenant `manifest.json` (version, schéma, identifiant de la clé, SHA-256 de chaque fichier), `lightmail.db` et `blobs/…`.

## Créer une sauvegarde

```bash
make backup
# ou
docker compose exec lightmail lightmail backup                    # → /data/backups/lightmail-AAAAMMJJ-HHMMSS.tar.gz
docker compose exec lightmail lightmail backup -out /data/backups/manuelle.tar.gz -with-cache
```

Copier l'archive hors du conteneur, puis hors du serveur :

```bash
docker compose cp lightmail:/data/backups/manuelle.tar.gz ./
# puis chiffrer et envoyer hors site (restic, borg, rclone + chiffrement…)
```

Les archives contiennent des données personnelles (en-têtes, corps en cache) : chiffrez-les avant tout stockage externe.

Exemple de tâche planifiée sur l'hôte (cron, tous les jours à 3 h) :

```cron
0 3 * * *  cd /opt/lightmail && docker compose exec -T lightmail lightmail backup >/dev/null
30 3 * * * find /var/lib/docker/volumes/lightmail_lightmail-data/_data/backups -name '*.tar.gz' -mtime +14 -delete
```

L'image n'a pas de shell : la purge des anciennes archives se fait depuis l'hôte, directement sur le volume. Le chemin dépend du nom du projet Compose (`docker volume inspect lightmail_lightmail-data`).

## Vérifier une sauvegarde

```bash
docker compose exec lightmail lightmail verify-backup /data/backups/manuelle.tar.gz
```

La vérification extrait l'archive dans un répertoire temporaire et contrôle :
- les SHA-256 de chaque fichier et l'absence de fichier inattendu ;
- le refus de tout nom d'entrée dangereux (`../`, chemins absolus, types non réguliers) ;
- `PRAGMA integrity_check` sur la base ;
- que la **clé configurée** déchiffre bien les secrets de la sauvegarde (`keyMatches`).

Code de sortie non nul en cas de problème : utilisable dans une supervision.

## Restaurer

```bash
make restore FILE=/data/backups/manuelle.tar.gz
# ou, étape par étape :
docker compose stop lightmail
docker compose run --rm --no-deps lightmail restore /data/backups/manuelle.tar.gz
docker compose start lightmail
```

`restore` :
1. refuse de s'exécuter si le serveur tourne (verrou sur le répertoire de données) ;
2. vérifie entièrement l'archive (sommes, intégrité, clé) avant de toucher quoi que ce soit ;
3. refuse une sauvegarde dont le schéma est plus récent que la version installée ;
4. déplace la base actuelle dans `/data/pre-restore-<date>/`, ce qui permet de revenir en arrière ;
5. installe la base et les fichiers.

Au redémarrage, LightMail resynchronise de façon incrémentale.

## Migrer vers un autre VPS

1. Ancien serveur : `make backup`, puis copiez l'archive **et** `secrets/master_key` (par un canal sûr) ainsi que `.env`.
2. Nouveau serveur : installez Docker, clonez le dépôt, replacez `.env` et `secrets/master_key`, puis `docker compose build`.
3. Copiez l'archive dans le volume : `docker compose up -d && docker compose cp manuelle.tar.gz lightmail:/data/backups/`.
4. `make restore FILE=/data/backups/manuelle.tar.gz`.
5. Mettez à jour le DNS. Si le domaine change, modifiez `LIGHTMAIL_PUBLIC_URL` **et** les URI de redirection des applications OAuth2.

## Procédure vérifiée

Ce cycle a été exécuté sur le déploiement Docker Compose de référence (voir [TESTING.md](TESTING.md#sauvegarde-et-restauration)) : paramètre modifié → sauvegarde à chaud → modification → arrêt → restauration → redémarrage, valeur sauvegardée retrouvée. Vérification avec une mauvaise clé : `keyMatches: false`, échec. Démarrage avec une mauvaise clé : refusé.
