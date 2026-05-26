{{- define "compliance-service.fullname" -}}
{{- .Release.Name }}-compliance-service
{{- end }}

{{- define "compliance-service.labels" -}}
app.kubernetes.io/name: compliance-service
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "compliance-service.selectorLabels" -}}
app.kubernetes.io/name: compliance-service
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "compliance-service.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- .Release.Name }}-compliance-service
{{- else }}
default
{{- end }}
{{- end }}
