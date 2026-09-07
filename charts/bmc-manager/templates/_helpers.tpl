{{- define "bmc-manager.name" -}}
{{ .Chart.Name }}
{{- end }}

{{- define "bmc-manager.labels" -}}
app.kubernetes.io/name: {{ include "bmc-manager.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "bmc-manager.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bmc-manager.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
