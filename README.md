# qStack

Собственный облачный провайдер: IaaS (вычисления, сети, диски, объектное хранилище, балансировщики), IAM и биллинг.

Control plane написан на Go. Под капотом: Debian 13, KVM/QEMU + libvirt, OVN/OVS, Ceph (RBD + RGW) или локальные диски (LVM).

Как в Proxmox: compute-узлы подключаются одной командой (`qs-node join`), веб-консоль доступна на любом узле. Control plane никогда не работает на compute-узлах: он разворачивается Helm-чартом в отдельном Kubernetes-кластере (минимально — одна машина с k3s).

## Разработка

Код можно писать на любой ОС (в том числе Windows); сборка и тесты идут на Linux в поде k3s через [DevSpace](https://www.devspace.sh/).

Нужно: Go ≥ 1.26, [DevSpace](https://www.devspace.sh/docs/getting-started/installation) и kubeconfig с доступом к dev-namespace (`qstack-dev`).

```bash
devspace dev           # развернуть dev-под, включить синхронизацию, открыть shell
devspace run verify    # make verify внутри пода
```

Внутри пода (или на Linux/CI напрямую):

| Команда | Что делает |
|---|---|
| `make gen` | генерация кода (buf, sqlc) |
| `make lint` | golangci-lint |
| `make test` | unit-тесты (`-race`) |
| `make test-integration` | интеграционные тесты (тег `integration`) |
| `make licenses` | проверка лицензий зависимостей (совместимость с Apache-2.0) |
| `make verify` | всё, что проверяет CI |

Версии инструментов закреплены в `tools/go.mod` и `tools/protoc/go.mod` и запускаются через `go tool`, глобально ничего ставить не нужно.

## Статус

Фаза 0 — фундамент монорепо.

## Лицензия

[Apache License 2.0](LICENSE).
