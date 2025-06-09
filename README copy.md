# Monitoring GnoLand Validators

The goal is to set up Grafana dashboards with an alert system to detect when a validator is failing.

First, I had to understand the logic of a validator on Gnoland. I followed the Gnoland setup guide to set up a local validator. Then, I explored the logs to see if there was any data that could be transformed into metrics for Prometheus. While navigating through the configuration, I found that telemetry could be activated, and Gnoland provided Grafana dashboards in the misc/telemetry folder. It was also necessary to use OLTP to transform the metrics and expose them to Prometheus.

I then created a docker-compose file with two services: OLTP and a Gnoland validator. I deployed this on the monitoring server to begin my tests. Of course, I had previously installed Grafana, Prometheus, Docker, and node_exporter on the monitoring server as services.

Once the telemetry was enabled, I realized there was no information regarding missed blocks, which needed to be calculated. The necessary information was available through the validator's RPC, in the status and validator sections. I then created a Go program to calculate and expose these metrics to Prometheus. I containerized this program in Docker and integrated it into my docker-compose file.

Finally, I started configuring Grafana for alerts, with CPU stress tests.

---

## Architecture

---

## List of Alerts

- Missing block :
    To know if a block is missing is simple: it's the difference between the total number of blocks received and the number of blocks signed. The metrics provided via telemetry do not give this information.

    Using RPC to Retrieve Metrics:
     To get these metrics, I used the RPC APIs of Gnoland, such as the status API (to get the latest block number) and the validators API (to retrieve validation information, particularly signed blocks).

    Programming in Go to Expose Metrics: I wrote a Go program that makes HTTP requests to the Gnoland RPC API to retrieve this information, processes it, and exposes the results as Prometheus metrics.

        Created Metrics:

        - gnoland_total_blocks: The total number of blocks received.
        - gnoland_signed_blocks: The number of blocks signed by the validator.
        - gnoland_missed_blocks: The number of blocks missed by the validator.

    Setting up the Exporter with Docker: I created a Dockerfile to containerize my Go program and make it available in a Docker container.

        - I used a Golang base image to build the Go program.
        - After building the program, I created a lighter final image based on Alpine for running the program.

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

## Install Prometheus / Grafana / node_exporter with Ansible

## Configure Data Sources of Grafana

## Set Up Dashboards

## Configure Alerts

## Recommandation

The naming of targets in Prometheus is important for several reasons, notably for effective management of alerts, queries, and dashboards in Grafana.

Here's why naming targets plays a crucial role:

- **Data Organization**: The name of the targets makes it easy to identify and organize data. For example, naming a target as gnoland_validator_1 allows you to quickly distinguish different validators or services. This helps avoid confusion when there are multiple data sources in Prometheus.

- **Selection in Grafana**: When configuring panels in Grafana, the target's name plays an important role in correctly retrieving metrics. If a target is misnamed, it may become difficult to select the right metrics or create queries to display data in the dashboards.

- **Alerts and Filtering**: Prometheus alerts can be configured to be triggered by specific conditions on particular targets. If the target names are inconsistent, it can complicate alert management, making it harder to monitor specific services or validators.

##### Best Practices for Naming Targets in Prometheus

Here are some good practices to follow when naming your targets in Prometheus:

- **Precise and Descriptive**: Use names that are precise and easy to understand. For example, for a validator, use gnoland_validator_1 instead of validator_1, especially if you have multiple validators.

- **Use Labels**: In addition to the target name, Prometheus allows you to add labels to distinguish different aspects of the same target. For example, for a validator, you can use a label like validator="gnoland_validator_1". This will allow you to filter metrics more easily based on these labels in Grafana.

- **Consistent Format**: If you're monitoring multiple services, make sure to adopt a consistent naming convention. For example, if monitoring multiple validators, you could have targets like gnoland_validator_1, gnoland_validator_2, etc.

- **Include Useful Details**: You can include information about the version or environment of the validator in the target name. For example: gnoland_validator_1_v1.0 or gnoland_validator_1_prod.

By following these recommendations, you simplify the management of targets in Prometheus, and this makes using Grafana smoother and more consistent for creating dashboards and alerts
