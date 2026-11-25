# boostr

Rent GPU machines on [vast.ai](https://vast.ai) from the command line. One Go binary, standard library only.

```bash
go install github.com/guregodevo/boostr@latest
export VAST_API_KEY=...          # or: vastai set api-key ...
boostr key add                    # your ~/.ssh public key, once per account
```

## Commands

| Command | Does |
|---|---|
| `boostr offers [filters]` | the cheapest matching offers; needs no key |
| `boostr up --image IMG --disk GB [filters]` | rents the cheapest match, waits until it runs, prints the ssh command |
| `boostr up … --with janis,memdoor` | also installs them, and returns when they are ready (below) |
| `boostr up … --onstart SCRIPT --ready` | runs your own script at start and waits until it writes `/root/boostr-ready`; a script that writes `/root/boostr-failed` ends the wait with the end of `/root/boostr-setup.log` |
| `boostr deploy ID --with janis,memdoor` | installs them on a running instance, or upgrades what has another version |
| `boostr recipe janis,memdoor` | prints the setup script `--with` runs |
| `boostr wait ID [--timeout 1h]` | the same wait for an instance already rented or started again |
| `boostr ls` | your instances: state, $/h, uptime, ssh address |
| `boostr ssh ID [command]` | ssh into one |
| `boostr logs ID [--tail N]` | its container log |
| `boostr stop ID` | destroy it and its disk: a stopped box bills its disk, and its host may rent the GPU out before it starts again |
| `boostr stop ID --keep` / `boostr start ID` | stop it and keep its disk (billed), or start a kept one again; when the host's GPUs are rented out, vast.ai queues the start, boostr says so, and `boostr stop --keep ID` cancels it |
| `boostr rm ID` | destroy it and its disk |
| `boostr credit` | the account's balance |

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

## janis and memdoor, ready to run

```bash
boostr up --gpu "RTX 5090" --with janis,memdoor    # returns when both are installed
boostr ssh ID 'janis serve --port 8093 --model Qwen3.8-27B-4bit'
boostr deploy ID --with janis,memdoor              # later: upgrade what has a newer version
boostr stop ID                                     # done: destroyed
boostr stop ID --keep                              # or kept: boostr start ID && boostr wait ID
```

A deployment is repeatable and idempotent:

| | |
|---|---|
| Repeatable | `up` and `deploy` resolve each moving name to an exact version before the box sees it: `--janis-ref` (default `main`) to a commit, the model's `--model-rev` (default `main`) to a revision, `--memdoor` (default `latest`) to a release tag. They print the versions and the flags that ask for the same ones again. Go is pinned (1.25.0), memdoor's release is checked against its SHA256SUMS. |
| Idempotent | the box keeps what is at its asked version and upgrades, or rolls back, what is not: janis is rebuilt only for another commit, a model file is fetched only when missing or short, memdoor reinstalled only for another release. A deploy of the same versions changes nothing. |
| Restart | a box kept with `boostr stop --keep` and started again is ready in seconds. `deploy` saves its script as `/root/boostr-setup.sh`, and a box started again runs that one: the last versions deployed, never the ones it was rented with. |

`--with` writes the script from the recipes in [`recipes/`](recipes); `boostr recipe janis,memdoor` prints it. The image defaults to `nvidia/cuda:12.9.1-devel-ubuntu22.04` and the disk to 60 GB. The box writes `/root/boostr-manifest` (what is installed), `/root/boostr-ready` when done, `/root/boostr-failed` and `/root/boostr-setup.log` on an error; `up` and `deploy` print the manifest, or the end of the log.

## janis on an H100

[`examples/janis-h100.sh`](examples/janis-h100.sh) builds [janis](https://github.com/guregodevo/janis) with CUDA and pulls `Qwen3.8-27B-4bit`.

```bash
boostr offers --gpu "H100 SXM" --limit 5
boostr up --gpu "H100 SXM" --image nvidia/cuda:12.9.1-devel-ubuntu22.04 --disk 150 \
  --onstart examples/janis-h100.sh --label janis-h100
boostr ssh ID 'tail -f /root/janis-setup.log'     # /root/janis-ready when done
boostr stop ID --keep                              # between runs
```

## Origin

The client is a port of memdoor's `pkg/provision` (removed 2026-09-18). It keeps the rules that came from failed rentals: the default filters above, a deleted instance reported as gone rather than as an outage, and an instance found by its label when vast.ai's create reply does not name it. The model fitting, vLLM recipe and billing of the old broker are not part of it.
