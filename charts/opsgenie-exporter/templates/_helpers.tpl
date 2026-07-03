{{/*
Expand the name of the chart.
*/}}
{{- define "opsgenie-exporter.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "opsgenie-exporter.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart label.
*/}}
{{- define "opsgenie-exporter.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "opsgenie-exporter.labels" -}}
helm.sh/chart: {{ include "opsgenie-exporter.chart" . }}
{{ include "opsgenie-exporter.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "opsgenie-exporter.selectorLabels" -}}
app.kubernetes.io/name: {{ include "opsgenie-exporter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "opsgenie-exporter.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "opsgenie-exporter.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Secret name holding the API key.
*/}}
{{- define "opsgenie-exporter.secretName" -}}
{{- if .Values.opsgenie.existingSecret }}
{{- .Values.opsgenie.existingSecret }}
{{- else }}
{{- include "opsgenie-exporter.fullname" . }}
{{- end }}
{{- end }}

{{/*
Secret key holding the API key.
*/}}
{{- define "opsgenie-exporter.secretKey" -}}
{{- if .Values.opsgenie.existingSecret }}
{{- .Values.opsgenie.existingSecretKey }}
{{- else }}
api-key
{{- end }}
{{- end }}
