{{- define "webhook-fanout.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "webhook-fanout.labels" -}}
app.kubernetes.io/name: webhook-fanout
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
