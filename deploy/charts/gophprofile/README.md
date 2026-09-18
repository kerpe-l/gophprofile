# gophprofile — Helm Chart

Chart разворачивает server, worker, Job миграций и, в dev-режиме, инфраструктуру
(PostgreSQL, MinIO, RabbitMQ) минимальными StatefulSet'ами. Модель конфигурации,
состав ресурсов и политика безопасности — раздел 9 спецификации проекта.

## Установка

Namespace создаётся вне chart'а и несёт лейблы Pod Security Standards:

```sh
kubectl create namespace gophprofile
kubectl label namespace gophprofile \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/warn=restricted
```

**Dev** (локальный кластер, инфраструктура внутри, образы собраны в containerd
кластера):

```sh
helm install gophprofile deploy/charts/gophprofile \
  -n gophprofile -f deploy/charts/gophprofile/values-dev.yaml
```

**Prod** (инфраструктура снаружи; Secret с `DATABASE_DSN`, `S3_ACCESS_KEY`,
`S3_SECRET_KEY`, `AMQP_URL` создаётся заранее):

```sh
helm install gophprofile deploy/charts/gophprofile \
  -n gophprofile -f deploy/charts/gophprofile/values-prod.yaml \
  --set secrets.existingSecret=<имя> \
  --set config.s3Endpoint=<host:port> \
  --set networkPolicy.externalEgress.cidr=<cidr инфраструктуры>
```

Порты egress к внешней инфраструктуре — `networkPolicy.externalEgress.ports`;
для S3 за HTTPS на стандартном порту нужен `--set ...ports.s3=443`. Регион
подписи S3 задаётся `config.s3Region`.

Обновление — `helm upgrade` с теми же аргументами: миграции выполняются
pre-upgrade hook'ом до перекатки подов. `helm uninstall` не удаляет PVC
инфраструктуры — данные переживают переустановку, чистка вручную:
`kubectl delete pvc --all -n gophprofile`.

## Ingress

По умолчанию — IngressClass кластера по умолчанию, без аннотаций. Под
ingress-nginx лимит тела запроса поднимается до лимита загрузки:

```sh
--set ingress.className=nginx \
--set-string ingress.annotations."nginx\.ingress\.kubernetes\.io/proxy-body-size"=10m
```

В NetworkPolicy пропускается трафик от контроллера из
`networkPolicy.ingressController` (по умолчанию traefik в `kube-system`,
как в k3s/Rancher Desktop); под другой контроллер значения меняются вместе.

## Мониторинг

ServiceMonitor'ы, PrometheusRule и ConfigMap дашбордов рассчитаны на Prometheus
Operator и включаются флагами `serviceMonitor.enabled`, `prometheusRule.enabled`,
`dashboards.enabled` — в `values-dev.yaml` все три включены. Локальный стек —
kube-prometheus-stack с `deploy/monitoring-values.yaml`: оператор выбирает
мониторы и правила без лейбла `release`, sidecar Grafana подхватывает дашборды
из всех namespace'ов. Для оператора с селекторами по лейблам —
`serviceMonitor.labels` и `prometheusRule.labels`.

Правила алертинга и дашборды лежат в `files/` и общие с docker compose.
