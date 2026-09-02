{{- define "janitor.name" -}}
{{ .Chart.Name }}
{{- end }}

{{- define "janitor.namespace" -}}
{{ .Values.janitor.namespace | default .Release.Namespace }}
{{- end }}

{{- define "janitor.labels" -}}
app.kubernetes.io/name: {{ include "janitor.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{- define "janitor.selectorLabels" -}}
app.kubernetes.io/name: {{ include "janitor.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
