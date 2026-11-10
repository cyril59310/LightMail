# Mise à jour

## Procédure standard

```bash
cd /opt/lightmail
make backup                                   # 1. sauvegarde préalable
docker compose cp lightmail:/data/backups ./backups-avant-maj   # copie hors conteneur
docker compose pull lightmail                 # 2. nouvelle image (LIGHTMAIL_VERSION dans .env, sinon latest)
docker compose up -d                          # 3. redémarrage (migrations automatiques)
docker compose logs -f lightmail              # 4. vérification
docker compose ps                             #    STATUS : healthy
```

Si vous avez figé la version (`LIGHTMAIL_VERSION=…` dans `.env`), changez-la d'abord. Les notes de chaque version sont sur la [page des releases](https://github.com/cyril59310/LightMail/releases). Le `docker-compose.yml` évolue rarement : comparez-le à celui joint à la nouvelle release.

Installation construite depuis les sources : remplacez l'étape 2 par `git pull && docker compose -f docker-compose.yml -f docker-compose.build.yml build`, et l'étape 3 par `make up-local`.

Au démarrage, les migrations de schéma (`backend/migrations/NNNN_*.sql`) sont appliquées dans l'ordre, chacune dans sa propre transaction. Une migration en échec annule ses modifications et empêche le démarrage : la base reste dans son état antérieur.

L'arrêt est propre : LightMail reçoit `SIGTERM`, termine les requêtes HTTP en cours (15 s maximum), arrête la synchronisation et **laisse aboutir les envois SMTP déjà commencés**. `stop_grace_period` vaut 30 s dans le compose. Un envoi interrompu par un arrêt brutal est marqué « incertain » au redémarrage, et non renvoyé automatiquement.

## Retour arrière

Les migrations ne sont pas réversibles automatiquement. Pour revenir à une version antérieure :

```bash
docker compose stop lightmail
# dans .env : LIGHTMAIL_VERSION=<version précédente>, puis :
docker compose pull lightmail
docker compose run --rm --no-deps lightmail restore /data/backups/<sauvegarde d'avant la mise à jour>.tar.gz
docker compose start lightmail
```

`restore` refuse une sauvegarde issue d'un schéma plus récent que la version installée.

## Vérifier la version

```bash
docker compose exec lightmail lightmail version
docker compose exec lightmail lightmail migrate     # affiche la version du schéma
```

La version est aussi visible dans *Paramètres → À propos*.

## Image multi-architecture

L'image publiée couvre amd64 et arm64 (le workflow de release la construit avec `docker buildx`). Pour votre propre registre :

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t registre/lightmail:0.1.0 --push .
```

Le binaire est compilé en Go pur, sans émulation (`--platform=$BUILDPLATFORM`) ; seule l'image finale dépend de l'architecture.
