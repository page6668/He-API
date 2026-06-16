{{/* payment-svc helpers — Story 1.7 B7 (mirrors auth-svc helpers). */}}

{{- define "payment-svc.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "payment-svc.fullname" -}}
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

{{- define "payment-svc.labels" -}}
app.kubernetes.io/name: {{ include "payment-svc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/part-of: he-api
{{- end -}}

{{- define "payment-svc.selectorLabels" -}}
app.kubernetes.io/name: {{ include "payment-svc.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app: payment-svc
{{- end -}}

{{- define "payment-svc.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "payment-svc.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
