{{/* sample-otel-app helpers — mirror of api-gateway (Story 1.3). */}}

{{- define "sample-otel-app.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "sample-otel-app.fullname" -}}
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

{{- define "sample-otel-app.labels" -}}
app.kubernetes.io/name: {{ include "sample-otel-app.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/part-of: he-api
{{- end -}}

{{- define "sample-otel-app.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sample-otel-app.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app: sample-otel-app
{{- end -}}

{{- define "sample-otel-app.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "sample-otel-app.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
