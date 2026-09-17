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

## Мониторинг

ServiceMonitor'ы рассчитаны на Prometheus Operator (локально —
kube-prometheus-stack, values в `deploy/monitoring-values.yaml`); без него —
`--set serviceMonitor.enabled=false`.
