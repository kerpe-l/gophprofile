{{- define "gophprofile.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end }}

{{- define "gophprofile.secretName" -}}
{{- if .Values.secrets.create -}}
gophprofile-secrets
{{- else -}}
{{- required "set secrets.existingSecret or secrets.create=true" .Values.secrets.existingSecret -}}
{{- end -}}
{{- end }}

{{- define "gophprofile.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end }}

{{- define "gophprofile.externalCidr" -}}
{{- required "set networkPolicy.externalEgress.cidr for egress outside the cluster" .Values.networkPolicy.externalEgress.cidr -}}
{{- end }}

{{/* Egress server и worker: podSelector при инфре в кластере, ipBlock — при внешней; OTLP — при заданном endpoint'е. */}}
{{- define "gophprofile.appEgress" -}}
{{- if .Values.postgres.enabled }}
- to:
    - podSelector:
        matchLabels:
          app: db
  ports:
    - port: 5432
{{- else }}
- to:
    - ipBlock:
        cidr: {{ include "gophprofile.externalCidr" . }}
  ports:
    - port: {{ .Values.networkPolicy.externalEgress.ports.postgres }}
{{- end }}
{{- if .Values.minio.enabled }}
- to:
    - podSelector:
        matchLabels:
          app: minio
  ports:
    - port: 9000
{{- else }}
- to:
    - ipBlock:
        cidr: {{ include "gophprofile.externalCidr" . }}
  ports:
    - port: {{ .Values.networkPolicy.externalEgress.ports.s3 }}
{{- end }}
{{- if .Values.rabbitmq.enabled }}
- to:
    - podSelector:
        matchLabels:
          app: rabbitmq
  ports:
    - port: 5672
{{- else }}
- to:
    - ipBlock:
        cidr: {{ include "gophprofile.externalCidr" . }}
  ports:
    - port: {{ .Values.networkPolicy.externalEgress.ports.amqp }}
{{- end }}
{{- with .Values.config.otlpEndpoint }}
{{- $port := splitList ":" . | last }}
{{- if not (regexMatch "^[0-9]+$" $port) }}
{{- fail "config.otlpEndpoint must be host:port" }}
{{- end }}
- to:
    - ipBlock:
        cidr: {{ include "gophprofile.externalCidr" $ }}
  ports:
    - port: {{ $port }}
{{- end }}
{{- end }}
