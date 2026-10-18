# Лаборатория

Место, где прогоняется то, что нельзя прогонять на рабочей машине: CI проекта, а
дальше — установка панели на чистую систему.

## Зачем self-hosted раннер

Облачные раннеры GitHub этому аккаунту недоступны. Дело не в конфигурации
workflow: GitHub отклоняет запуск с прямым текстом

> The job was not started because your account is locked due to a billing issue.

и не выдаёт задаче машину (`runner_id: 0`), хотя репозиторий публичный и минуты
ему не нужны. Self-hosted раннеры в биллинге не участвуют, поэтому проверки идут
на своей машине.

## Что где стоит

| Что | Где |
| --- | --- |
| Хост Proxmox | `proxmox`, 192.168.1.50, PVE 9.2 |
| Контейнер раннера | `ci-runner`, VMID 102, непривилегированный, 4 ядра, 4 ГБ, 16 ГБ |
| Образ | `local:vztmpl/ubuntu-24.04-standard_24.04-2_amd64.tar.zst` |
| Раннер | `/home/runner/actions-runner`, версия 2.338.0 |
| Служба | `actions.runner.vibecodinguser-hysteria-ward.pve-ci-runner.service` |
| Метка раннера | `hysteria-ward-ci` — по ней задачи находят эту машину |

`nesting=1` у контейнера обязателен: с systemd 255 Proxmox без него
предупреждает, что часть служб работать не будет.

## Почему в CI нет pull_request

Раннер стоит в домашней сети, а репозиторий публичный. Работа на `pull_request`
позволила бы любому, кто откроет PR, исполнить свой код на этой машине. В
`.github/workflows/ci.yml` остались только `push` в `main` и ручной запуск —
оба требуют прав на запись.

## Состояние раннера

```sh
# что видит GitHub
gh api repos/vibecodinguser/hysteria-ward/actions/runners \
  --jq '.runners[] | "\(.name): \(.status) busy=\(.busy)"'

# что происходит внутри
ssh proxmox "pct exec 102 -- systemctl status \
  'actions.runner.vibecodinguser-hysteria-ward.pve-ci-runner.service'"
```

## Если раннер отвалился

```sh
ssh proxmox "pct exec 102 -- systemctl restart \
  'actions.runner.vibecodinguser-hysteria-ward.pve-ci-runner.service'"
```

Совсем мёртвый контейнер пересоздаётся так — образ и пакеты те же, что при
первой сборке:

```sh
ssh proxmox "pct destroy 102 --force"
```

Дальше: создать контейнер из образа, включить `nesting`, поставить `git`, `make`,
`curl`, `file`, завести пользователя `runner`, скачать и распаковать раннер,
зарегистрировать его с меткой `hysteria-ward-ci`, поставить службой.

После `destroy` в GitHub остаётся offline-запись раннера — её можно снять:

```sh
gh api repos/vibecodinguser/hysteria-ward/actions/runners \
  --jq '.runners[] | select(.status=="offline") | .id' |
  xargs -n1 -I{} gh api --method DELETE \
  repos/vibecodinguser/hysteria-ward/actions/runners/{}
```

## Проверка, что CI жив

```sh
gh workflow run CI
gh run watch
```
