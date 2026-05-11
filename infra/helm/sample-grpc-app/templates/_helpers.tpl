{{/* sample-grpc-app helpers — scaffolded by scripts/scaffold-svc.sh (Story 1.5). */}}

{{- define "sample-grpc-app.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "sample-grpc-app.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $$name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $$name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $$name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "sample-grpc-app.labels" -}}
app.kubernetes.io/name: {{ include "sample-grpc-app.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/part-of: he-api
{{- end -}}

{{- define "sample-grpc-app.selectorLabels" -}}
app.kubernetes.io/name: {{ include "sample-grpc-app.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app: sample-grpc-app
{{- end -}}

{{- define "sample-grpc-app.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "sample-grpc-app.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
