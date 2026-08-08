# broker

Публикация с подтверждениями и потребление событий аватаров через RabbitMQ.

## Лестница повторов

Встроенного повтора с задержкой у RabbitMQ нет, поэтому backoff собран из очередей:

```
avatars.exchange (topic)
  └─ avatars.process           основная очередь; отклонённое → avatars.retry
avatars.retry (direct)
  ├─ avatars.retry.5s   ttl=5s  ┐ по истечении ttl → avatars.exchange
  ├─ avatars.retry.30s  ttl=30s ├ с ключом avatar.retried
  └─ avatars.retry.5m   ttl=5m  ┘
avatars.dlq (direct)
  └─ avatars.dead              исчерпавшие попытки
```

Ступень выбирает консьюмер: провалившееся сообщение он публикует в `avatars.retry`
с ключом ступени по номеру попытки (1-я → 5s, 2-я → 30s, дальше 5m) и подтверждает
исходное. После пяти попыток сообщение уходит в DLQ.

Устройство счётчика попыток, диспетчеризация по свойству `type` и привязки первой
ступени описаны в спецификации, [§6.2 Retry и DLQ](../../spec.md#62-retry-и-dlq).
