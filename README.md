# Monitoring GnoLand Validators

The goal is to set up Grafana dashboards with an alert system to detect when a validator is failing.

En premier,il as fallut comprendre la logique d un validateur sur gnoland. j'ai dábord suivis le setup guide de gnoland pour installer un validateur en local. J'ai par la suite chercher si il y avait des logs que je pouvais transformer en metrics pour prometheus.
Navigant dans la configuration je me suis apercu qu on pouvais activer des télemetrie. Et que gnoland nous fournissait dans le repertoire misc telemtry des dashboard Grafana et qu il fallait utilisé oltp pour transformer les metrics pour prometheus.

J ai donc crée un docker-compose avec deux service: OLTP et un validateur gnoland. J ai pousser cela sur le serveur monitorting pour commencer mes testes. Bien evidement au préalable j ai installer Grafana, Prometheus, docker et un node_exporter sur le serveur monitoring en tant que service.

Une fois les telemtry activer je me suis apercu qu'il n avait pas d'information sur les bloks perdu, il fallait les calculers. Les informations necesaire etant disponible via le rpc du validateur dans la parti status et Validateur.

 Une fois maitrisé cela en local, j ai construit l'image docker d'un validateur.

## List of Alerts

- Missing block :
To know if a block is missing is simple: it's the difference between the total number of blocks received and the number of blocks signed. The metrics provided via telemetry do not give this information. However, through the RPC, we can retrieve this data.

So, I created a Go code to expose these metrics to Prometheus.

- RAM swap usage
- CPU usage over 80%
- Disk usage over 80%

## Tools

- Docker
- Prometheus
- OLTP
- Node Exporter
- Discord

## Useful Links

- [GnoLand Validator Setup Guide](https://docs.gno.land/gno-infrastructure/validators/validators-setting-up-a-new-chain)

---

## Testing Environment

### Step 1: Clone GnoLand Repository and this repository

```sh
git clone git@github.com:gnolang/gno.git
git clone https://github.com/samouraiworld/gnomonitoring.git

```

### Step 2: Build GnoLand Docker Image and docker image of directory missing block

```sh
cd gno/
docker build -t gnoland-image --target=gnoland .
cd ../gnomonitoring/MissingBlock
./build.sh 
```

### Step 3: Compose docker

```sh
    docker compose up -d 
```

---

## Installation Steps

## Install Prometheus

## Install Grafana

## Configure Data Sources

## Set Up Dashboards

## Configure Alerts
