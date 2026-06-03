{{/* routing-svc helpers — Story 6.1. */}}

{{- define "routing-svc.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "routing-svc.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "routing-svc.labels" -}}
app.kubernetes.io/name: {{ include "routing-svc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/part-of: he-api
{{- end -}}

{{- define "routing-svc.selectorLabels" -}}
app.kubernetes.io/name: {{ include "routing-svc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app: routing-svc
{{- end -}}

{{- define "routing-svc.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "routing-svc.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
