{{- define "coordinator.name" -}}
{{ .Chart.Name }}
{{- end }}

{{- define "coordinator.labels" -}}
app.kubernetes.io/name: {{ include "coordinator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "coordinator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "coordinator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
