# Mise à jour

## Procédure standard

```bash
cd /opt/lightmail
make backup                                   # 1. sauvegarde préalable
docker compose cp lightmail:/data/backups ./backups-avant-maj   # copie hors conteneur
git pull                                      # 2. nouvelle version
docker compose build                          # 3. nouvelle image
docker compose up -d                          # 4. redémarrage (migrations automatiques)
docker compose logs -f lightmail              # 5. vérification
docker compose ps                             #    STATUS : healthy
```

Au démarrage, les migrations de schéma (`backend/migrations/NNNN_*.sql`) sont appliquées dans l'ordre, chacune dans sa propre transaction. Une migration en échec annule ses modifications et empêche le démarrage : la base reste dans son état antérieur.

L'arrêt est propre : LightMail reçoit `SIGTERM`, termine les requêtes HTTP en cours (15 s maximum), arrête la synchronisation et **laisse aboutir les envois SMTP déjà commencés**. `stop_grace_period` vaut 30 s dans le compose. Un envoi interrompu par un arrêt brutal est marqué « incertain » au redémarrage, et non renvoyé automatiquement.

## Retour arrière

Les migrations ne sont pas réversibles automatiquement. Pour revenir à une version antérieure :

```bash
docker compose stop lightmail
git checkout <version précédente> && docker compose build
docker compose run --rm --no-deps lightmail restore /data/backups/<sauvegarde d'avant la mise à jour>.tar.gz
docker compose start lightmail
```

`restore` refuse une sauvegarde issue d'un schéma plus récent que la version installée.

## Vérifier la version

```bash
docker compose exec lightmail lightmail version
docker compose exec lightmail lightmail migrate     # affiche la version du schéma
```

La version est aussi visible dans *Paramètres → État du système*.

## Image multi-architecture

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t registre/lightmail:0.1.0 --push .
```

Le binaire est compilé en Go pur, sans émulation (`--platform=$BUILDPLATFORM`) ; seule l'image finale dépend de l'architecture.
