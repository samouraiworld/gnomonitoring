# Monitoring GnoLand Validators

The goal is to set up Grafana dashboards with an alert system to detect when a validator is failing.

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

# Testing Environment

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
### Step 3: Clone This Repository and compose 
```sh

```






---

# Installation Steps

## Install Prometheus

## Install Grafana

## Configure Data Sources

## Set Up Dashboards

## Configure Alerts

