{{- define "resolver.name" -}}
{{ .Chart.Name }}
{{- end }}

{{- define "resolver.labels" -}}
app.kubernetes.io/name: {{ include "resolver.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "resolver.selectorLabels" -}}
app.kubernetes.io/name: {{ include "resolver.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
