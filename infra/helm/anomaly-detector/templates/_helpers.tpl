{{/*
Expand the name of the chart.
*/}}
{{- define "anomaly-detector.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "anomaly-detector.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "anomaly-detector.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
app.kubernetes.io/name: {{ include "anomaly-detector.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "anomaly-detector.selectorLabels" -}}
app.kubernetes.io/name: {{ include "anomaly-detector.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "anomaly-detector.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- include "anomaly-detector.fullname" . }}
{{- else }}
default
{{- end }}
{{- end }}
