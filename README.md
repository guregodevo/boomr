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
| `boostr ls` | your instances: state, $/h, uptime, ssh address |
| `boostr ssh ID [command]` | ssh into one |
| `boostr logs ID [--tail N]` | its container log |
| `boostr stop ID` / `boostr start ID` | stop it (only the disk is billed) or start it again on the same disk; when the host's GPUs are rented out, vast.ai queues the start, boostr says so, and `boostr stop ID` cancels it |
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

## janis on an H100

[`examples/janis-h100.sh`](examples/janis-h100.sh) builds [janis](https://github.com/guregodevo/janis) with CUDA and pulls `Qwen3.8-27B-4bit`.

```bash
boostr offers --gpu "H100 SXM" --limit 5
boostr up --gpu "H100 SXM" --image nvidia/cuda:12.9.1-devel-ubuntu22.04 --disk 150 \
  --onstart examples/janis-h100.sh --label janis-h100
boostr ssh ID 'tail -f /root/janis-setup.log'     # /root/janis-ready when done
boostr stop ID                                     # between runs
```

## Origin

The client is a port of memdoor's `pkg/provision` (removed 2026-09-18). It keeps the rules that came from failed rentals: the default filters above, a deleted instance reported as gone rather than as an outage, and an instance found by its label when vast.ai's create reply does not name it. The model fitting, vLLM recipe and billing of the old broker are not part of it.
