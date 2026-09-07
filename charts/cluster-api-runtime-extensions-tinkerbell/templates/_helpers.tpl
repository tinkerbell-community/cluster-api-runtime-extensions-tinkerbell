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

{{- /*
carext.tootlesUserDataURL mirrors resolve.TootlesUserDataURL: the tootles base
URL, or http://<tinkerbellIP>:7080 when only the IP is set, plus the EC2-style
user-data path CAPT's machine configuration is served at.
*/}}
{{- define "carext.tootlesUserDataURL" -}}
{{- $base := trimSuffix "/" .Values.resolver.tootlesURL -}}
{{- if and (not $base) .Values.resolver.tinkerbellIP -}}
{{- $base = printf "http://%s:7080" .Values.resolver.tinkerbellIP -}}
{{- end -}}
{{- if not $base -}}
{{- fail "template.enabled requires resolver.tootlesURL or resolver.tinkerbellIP to build the talos.config URL" -}}
{{- end -}}
{{- printf "%s/2009-04-04/user-data" $base -}}
{{- end }}
