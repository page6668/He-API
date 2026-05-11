{{/*
Helpers for the db-doctor chart.
*/}}

{{- define "db-doctor.name" -}}
db-doctor
{{- end }}

{{- define "db-doctor.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "db-doctor.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "db-doctor.labels" -}}
app.kubernetes.io/name: {{ include "db-doctor.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
he-api/story: "1.6"
{{- end }}

{{- define "db-doctor.selectorLabels" -}}
app.kubernetes.io/name: {{ include "db-doctor.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
