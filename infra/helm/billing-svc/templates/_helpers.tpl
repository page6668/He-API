{{/* billing-svc helpers — Story 7.2 (mirrors auth-svc helpers). */}}

{{- define "billing-svc.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "billing-svc.fullname" -}}
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

{{- define "billing-svc.labels" -}}
app.kubernetes.io/name: {{ include "billing-svc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/part-of: he-api
{{- end -}}

{{- define "billing-svc.selectorLabels" -}}
app.kubernetes.io/name: {{ include "billing-svc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app: billing-svc
{{- end -}}

{{- define "billing-svc.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "billing-svc.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
