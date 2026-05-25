{{- define "live-feed-broadcaster.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "live-feed-broadcaster.labels" -}}
app.kubernetes.io/name: live-feed-broadcaster
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
