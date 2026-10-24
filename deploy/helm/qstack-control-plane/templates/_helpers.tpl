{{- define "qstack.labels" -}}
app.kubernetes.io/part-of: qstack
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "qstack.selector" -}}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/name: {{ .name }}
{{- end -}}

{{/* Image of a qStack binary: registry/binary:tag. */}}
{{- define "qstack.image" -}}
{{ .root.Values.images.registry }}/{{ .binary }}:{{ .root.Values.images.tag }}
{{- end -}}

{{/* Restricted pod security (PSS "restricted"). */}}
{{- define "qstack.podSecurity" -}}
runAsNonRoot: true
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "qstack.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end -}}
