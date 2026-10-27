# Synthetic full-backend rehearsal

This opt-in suite runs the unchanged backend binary on a newly generated,
restored invented archive. RPC, GraphQL/WebSocket and both Telegram bots use
local protocol fixtures. It never loads an operator dump, production config,
token or endpoint. Images are built locally; no registry login or publication
is performed.

The runtime owns every container, volume and image tag it creates. The mock
container is the `network:none` namespace anchor; backend and probes share that
namespace and communicate over loopback. PostgreSQL retains its separate
`network:none` namespace and exposes only the owned clone's Unix socket.
The generic restore role retains its eight-connection cap. Only this fresh
application clone receives a fixed cap of 24 connections for the unchanged
backend's 20-connection pool plus probes; PostgreSQL stays capped at 30. The
sealed marker, exact owned server and unchanged nonprivileged role flags are
verified before and after this adjustment. This is a test resource budget, not
a proposed production setting.

Containers are inspected before start for image ID, namespace, mounts, user,
capabilities, NoNewPrivs and absence of host ports. Network probes verify the
absence of external routes. Telegram uses a fixture-only TLS CONNECT proxy and
a temporary CA inside the owned configuration volume; certificate verification
remains enabled and the proxy never forwards traffic.

## Run

Requirements: Python 3.10+, local Linux Docker engine with Compose, Git, and
network access during builds to retrieve dependencies. Runtime processes have
no external connectivity. Docker Desktop is supported on macOS through its
explicit local socket. Inherited private-directory and remote-context checks
continue to apply.

Without the opt-in environment variable the integration tests are skipped:

```sh
python3 -B -W error::ResourceWarning -m unittest discover -s rehearsal/application -p 'test_*.py' -v
```

Run the finite acceptance suite using only generated fixtures:

```sh
RUN_SYNTHETIC_APPLICATION_TESTS=1 python3 -B -W error::ResourceWarning -m unittest discover -s rehearsal/application -p 'test_synthetic_*.py' -v
```

By default, private command captures and generated archives are temporary. To
retain diagnostics, set `SYNTHETIC_APPLICATION_PRIVATE_OUTPUT` to an owned
canonical mode-0700 directory outside Git; each test class creates a new child.
Never upload or commit those captures, SQL, configs, receipts or archives. A
completed summary is written only after verified cleanup. A failed or interrupted
scenario must not create a completed application summary.

## Finite acceptance cases

- The actual backend runs InitDB and starts both chain workers, GovDAO, bot
  command loops, metrics, aggregator, scheduler and public API. The nominal
  scenario proves worker DB progress on both chains, the immediate completed
  metrics cycle and protocol calls; a distinct WebSocket proposal is persisted.
- A subsequent 600-block jump exercises the `>500` blocking-backfill branch and
  requires every height in that interval to be collected.
- After collection is proved, a separate explicit report fixture replaces only
  the two invented chains. Its heights are above the now-fixed RPC tips. The
  normal persisted one-hour valset-refresh setting keeps it stable during the
  bounded test. Data timestamps use a UTC reference captured during execution.
- Public reports verify four periods, a perfect validator score, voting power,
  proposer/signing rates, last-alert recency, milliseconds, measured zero versus
  unavailable latency, a negative BFT margin, stale BFT unavailability and chain
  separation. Cache tests check exact-address filters, immutable cached rosters,
  `Cache-Control: no-store`, a real 30-second expiration and refreshed payloads.
- Invented Telegram updates exercise exact address, exact moniker, ambiguous
  moniker and per-chat active chain selection. Only the mock receives replies.
- Two owned local image IDs contain the same approved backend binary with
  different rehearsal metadata. Baseline → candidate → baseline verifies exact
  image selection, stopped DB connections and API restart. The explicit report
  fixture is reintroduced after each startup to repeat health/consensus contract
  smokes. Before this reset, the suite requires unchanged participation counts
  and payloads and verifies the expected zeroing of departed fixture voting
  power by the real valset refresh. These observations are recorded. The
  separate archive probe must also stay unchanged across all switches. The duplicate
  `./server` argument is observed separately. This proves neither compatibility
  between different application versions nor a production rollback.
- A callback failure prevents completion. A later attempt must restore into new
  project/clone identities, start successfully and clean up independently.

The summary remains `synthetic_only:true`. Source/archive equality is checked
before migration and application writes; it does not assert equality after those
writes. InitDB reductions in the inherited invented fixture remain explicitly
marked for review. Expected report-fixture replacement and application writes
are not presented as accidental restore loss.

Protected-route identity-provider flows and successful outbound Slack/Discord
webhooks are outside this suite. The SSRF guard is never weakened to make a
local webhook succeed. No production snapshot compatibility, database rollback,
capacity, RPO or RTO is established. Production backup/restore preparation,
verified release artifacts and explicit release authorization remain separate.
