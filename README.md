# Monitoring GnoLand Validators

The goal is to set up Grafana dashboards with an alert system to detect when a validator is failing.

## List of Alerts
- Missing block
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

### Step 1: Clone GnoLand Repository
```sh
git clone git@github.com:gnolang/gno.git
```

### Step 2: Build GnoLand Docker Image
```sh
docker build -t gnoland-image --target=gnoland .
```

### Step 3: Clone This Repository
```sh
git clone <repository_url>
```

---

# Installation Steps

## Install Prometheus

## Install Grafana

## Configure Data Sources

## Set Up Dashboards

## Configure Alerts

