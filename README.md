# hyperscalr

Rents GPU boxes on [vast.ai](https://vast.ai), installs [janis](https://github.com/guregodevo/janis) and [memdoor](https://github.com/guregodevo/memdoor) on them, and serves them as one OpenAI-compatible endpoint. One Go binary, standard library only. Formerly boostr.

| | |
|---|---|
| One command for N boxes | `up --count N` rents the N cheapest offers that match the filters and waits until each has janis, the model and memdoor installed |
| One endpoint | `pool serve` takes `/v1/chat/completions` and forwards each request to a box's janis, streaming |
| A run stays on one box | the run (`X-Memdoor-Run`) is placed by consistent hashing with bounded load; its later requests go to the same box, where its prompt cache is |
| Freed at the end | memdoor posts `/v1/run/done` when a run ends; the run's place on its box is released |
| Idempotent | `pool add` of a box in the ring whose janis answers is a no-op; `up --with` and `deploy` of the same versions change nothing |

## Getting started

```bash
go install github.com/guregodevo/hyperscalr@latest
export VAST_API_KEY=...                         # or: vastai set api-key ...
hyperscalr key add                              # your ~/.ssh public key, once per account
```

1. Rent the boxes. The command returns when every box is ready and prints their ids.

   ```bash
   hyperscalr up --count 10 --gpu "A40,RTX 6000Ada" --min-gpu-ram 45 --max-price 1.2 \
     --with janis,memdoor --model Inkling-2-Mini-4bit
   ```

2. Start janis on each box.

   ```bash
   for id in ID1 ID2 ...; do
     hyperscalr ssh $id 'JANIS_PARALLEL=2 JANIS_GGML_DEVICE=gpu setsid -f janis serve --host 127.0.0.1 --port 8093 --model Inkling-2-Mini-4bit >> /root/janis.log 2>&1'
   done
   ```

3. Serve the pool and add the boxes. `pool add` opens an ssh tunnel to each box's janis and checks `/health`.

   ```bash
   hyperscalr pool serve &                       # http://127.0.0.1:18900
   hyperscalr pool add ID1 ID2 ...
   hyperscalr pool ls
   ```

4. Point memdoor at the pool as a company AI gateway, then run as usual.

   ```bash
   export AI_GATEWAY_BASE_URL=http://127.0.0.1:18900/v1 AI_GATEWAY_API=chat AI_GATEWAY_TOKEN=unused
   export MEMDOOR_MODEL=Inkling-2-Mini-4bit
   memdoor gateway &
   memdoor workflow run <workflow>                # one run: one box
   ```

5. Destroy the boxes.

   ```bash
   hyperscalr stop ID                            # destroys the instance and its disk
   ```

## The pool

| | |
|---|---|
| Key | `X-Memdoor-Run`: memdoor's workflow run for every step of it, else the turn's run, else the conversation (memdoor #414) |
| Placement | SHA-256 ring, 150 virtual nodes a box; a new run goes to the first box clockwise from its key under the bound, ceil(1.25 × (runs + 1) / boxes) |
| Sticky | a placed run keeps its box while the box is in the ring, drained or not |
| Release | `POST /v1/run/done?run=<key>`; memdoor sends it at the end of a workflow run or a turn |
| Members | `POST /add`, `/drain`, `/remove`; `GET /ring`. `pool add` and `pool ls` use them |
| State | in memory: a restarted pool starts empty |

The scale loop (`pool serve --min --max --idle`) logs what it would rent or drain. `--act` is refused: renting and destroying from the loop are not wired.

### One run, 2026-11-28

| | |
|---|---|
| Work | 64 LiveCodeBench problems, single-call workflow, Inkling 2 Mini 4-bit on janis v0.1.24 |
| Boxes | 10, A40 / RTX 6000 Ada / A100, $0.56-0.94/h, `JANIS_PARALLEL=2` |
| Concurrency | 10 runs at a time from one memdoor gateway |
| Wall clock | 84 min; median problem 587 s |
| Cost | $21.54 for the 10 boxes (vast.ai charges), about 2 h 10 min each from rental to destroy, setup included |

Without run done the pool counted finished runs, put three live runs on a 2-slot box, and three replies stalled. With run done, reruns sat one a box and the ring was back to 0 runs at the end.

## Commands

| Command | Does |
|---|---|
| `hyperscalr offers [filters]` | the cheapest matching offers; needs no key |
| `hyperscalr up --image IMG --disk GB [filters]` | rents the cheapest match, waits until it runs, prints the ssh command |
| `hyperscalr up … --count N` | rents the N cheapest matching offers in one command, one instance each, and waits for each |
| `hyperscalr up … --with janis,memdoor` | also installs them, and returns when they are ready (below) |
| `hyperscalr up … --onstart SCRIPT --ready` | runs your own script at start and waits until it writes `/root/hyperscalr-ready`; a script that writes `/root/hyperscalr-failed` ends the wait with the end of `/root/hyperscalr-setup.log` |
| `hyperscalr deploy ID --with janis,memdoor` | installs them on a running instance, or upgrades what has another version |
| `hyperscalr recipe janis,memdoor` | prints the setup script `--with` runs |
| `hyperscalr wait ID [--timeout 1h]` | the same wait for an instance already rented or started again |
| `hyperscalr ls` | your instances: state, $/h, uptime, ssh address |
| `hyperscalr ssh ID [command]` | ssh into one |
| `hyperscalr logs ID [--tail N]` | its container log |
| `hyperscalr stop ID` | destroy it and its disk: a stopped box bills its disk, and its host may rent the GPU out before it starts again |
| `hyperscalr stop ID --keep` / `hyperscalr start ID` | stop it and keep its disk (billed), or start a kept one again; when the host's GPUs are rented out, vast.ai queues the start, hyperscalr says so, and `hyperscalr stop --keep ID` cancels it |
| `hyperscalr rm ID` | destroy it and its disk |
| `hyperscalr credit` | the account's balance |
| `hyperscalr pool serve [--addr 127.0.0.1:18900]` | the ring memdoor sends its requests to (`AI_GATEWAY_BASE_URL=http://127.0.0.1:18900/v1`, `AI_GATEWAY_API=chat`); each run (`X-Memdoor-Run`) stays on one box, placed by consistent hashing |
| `hyperscalr pool add ID...` | opens an ssh tunnel to each box's janis (`--port 8093`), checks `/health`, adds it to the ring; a box already in the ring whose janis answers is left as it is |
| `hyperscalr pool ls` | the ring's boxes, their endpoints and runs |

## Filters

| Flag | Default | Why |
|---|---|---|
| `--gpu "H100 SXM,H100 PCIE"` | any | names as vast.ai writes them |
| `--gpus N` | 1 | GPUs per machine |
| `--min-gpu-ram GB` | none | memory per GPU |
| `--max-price $/h` | none | the whole machine's rate |
| `--min-disk GB` | 60, or `--disk` | too small and the container dies mid-download |
| `--min-down Mb/s` | 500 | a slow link is paid for while it downloads |
| `--min-cuda V` | 12.8 | an older driver fails the image at start (CUDA error 804) |
| `--exclude-geo CN,HK` | CN,HK | `""` for none |
| `--unverified` | off | unverified hosts are cheaper and likelier to vanish |
| `--bid` | off | the interruptible tier, at the offer's minimum bid unless `--bid-price` |

An image must be pinned to a tag or a digest. `:latest` is refused: a moving tag cannot be reproduced.

## A deployment: janis and memdoor

```bash
hyperscalr up --gpu "RTX 5090" --with janis,memdoor    # returns when both are installed
hyperscalr ssh ID 'janis serve --port 8093 --model Qwen3.8-27B-4bit'
hyperscalr deploy ID --with janis,memdoor              # later: upgrade what has a newer version
hyperscalr stop ID                                     # done: destroyed
hyperscalr stop ID --keep                              # or kept: hyperscalr start ID && hyperscalr wait ID
```

A deployment is repeatable and idempotent:

| | |
|---|---|
| Repeatable | `up` and `deploy` resolve each moving name to an exact version before the box sees it: `--janis-ref` (default `main`) to a commit, the model's `--model-rev` (default `main`) to a revision, `--memdoor` (default `latest`) to a release tag. They print the versions and the flags that ask for the same ones again. Go is pinned (1.25.0), memdoor's release is checked against its SHA256SUMS. |
| Idempotent | the box keeps what is at its asked version and upgrades, or rolls back, what is not: janis is rebuilt only for another commit, a model file is fetched only when missing or short, memdoor reinstalled only for another release. A deploy of the same versions changes nothing. |
| Restart | a box kept with `hyperscalr stop --keep` and started again is ready in seconds. `deploy` saves its script as `/root/hyperscalr-setup.sh`, and a box started again runs that one: the last versions deployed, never the ones it was rented with. |

`--with` writes the script from the recipes in [`recipes/`](recipes); `hyperscalr recipe janis,memdoor` prints it. The image defaults to `nvidia/cuda:12.9.1-devel-ubuntu22.04` and the disk to 60 GB. The box writes `/root/hyperscalr-manifest` (what is installed), `/root/hyperscalr-ready` when done, `/root/hyperscalr-failed` and `/root/hyperscalr-setup.log` on an error; `up` and `deploy` print the manifest, or the end of the log.

## janis on an H100

[`examples/janis-h100.sh`](examples/janis-h100.sh) builds [janis](https://github.com/guregodevo/janis) with CUDA and pulls `Qwen3.8-27B-4bit`.

```bash
hyperscalr offers --gpu "H100 SXM" --limit 5
hyperscalr up --gpu "H100 SXM" --image nvidia/cuda:12.9.1-devel-ubuntu22.04 --disk 150 \
  --onstart examples/janis-h100.sh --label janis-h100
hyperscalr ssh ID 'tail -f /root/janis-setup.log'     # /root/janis-ready when done
hyperscalr stop ID --keep                              # between runs
```

## Origin

The client is a port of memdoor's `pkg/provision` (removed 2026-09-18). It keeps the rules that came from failed rentals: the default filters above, a deleted instance reported as gone rather than as an outage, and an instance found by its label when vast.ai's create reply does not name it. The model fitting, vLLM recipe and billing of the old broker are not part of it.
