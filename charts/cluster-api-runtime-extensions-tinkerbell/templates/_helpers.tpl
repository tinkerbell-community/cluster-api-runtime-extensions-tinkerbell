{{- define "carext.name" -}}
{{ .Chart.Name }}
{{- end }}

{{- define "carext.labels" -}}
app.kubernetes.io/name: {{ include "carext.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "carext.selectorLabels" -}}
app.kubernetes.io/name: {{ include "carext.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
