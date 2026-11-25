{{- define "assay.fullname" -}}
{{- printf "%s-assay" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "assay.labels" -}}
app.kubernetes.io/name: assay
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "assay.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) -}}
{{- end -}}

{{- define "assay.databaseSecret" -}}
{{- default (printf "%s-database" (include "assay.fullname" .)) .Values.database.existingSecret -}}
{{- end -}}

{{- define "assay.keysSecret" -}}
{{- default (printf "%s-keys" (include "assay.fullname" .)) .Values.keys.existingSecret -}}
{{- end -}}

{{/* The variables of the web and the worker pods. The role comes first. */}}
{{- define "assay.env" -}}
- name: ASSAY_DATABASE_URL
  valueFrom: {secretKeyRef: {name: {{ include "assay.databaseSecret" .root }}, key: ASSAY_DATABASE_URL}}
- name: ASSAY_SECRET_KEY
  valueFrom: {secretKeyRef: {name: {{ include "assay.keysSecret" .root }}, key: ASSAY_SECRET_KEY}}
- name: ASSAY_SETUP_TOKEN
  valueFrom: {secretKeyRef: {name: {{ include "assay.keysSecret" .root }}, key: ASSAY_SETUP_TOKEN, optional: true}}
{{- if .root.Values.baseURL }}
- name: ASSAY_BASE_URL
  value: {{ .root.Values.baseURL | quote }}
{{- end }}
{{- range $k, $v := merge (deepCopy .env) .root.Values.env }}
- name: {{ $k }}
  value: {{ $v | quote }}
{{- end }}
{{- end -}}
