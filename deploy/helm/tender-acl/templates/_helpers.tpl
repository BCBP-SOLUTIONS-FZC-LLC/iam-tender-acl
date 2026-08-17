{{/*
Expand the name of the chart.
*/}}
{{- define "tender-acl.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to
this (by the DNS naming spec).
*/}}
{{- define "tender-acl.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "tender-acl.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to every resource.
*/}}
{{- define "tender-acl.labels" -}}
helm.sh/chart: {{ include "tender-acl.chart" . }}
{{ include "tender-acl.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels — used by Deployment, Service, HPA, PDB, and ServiceMonitor.
*/}}
{{- define "tender-acl.selectorLabels" -}}
app.kubernetes.io/name: {{ include "tender-acl.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use.
*/}}
{{- define "tender-acl.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "tender-acl.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Image tag: prefer .Values.image.tag; fall back to chart appVersion.
*/}}
{{- define "tender-acl.imageTag" -}}
{{- .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
Name of the Secret holding application secrets: either a pre-existing,
externally-managed Secret (.Values.existingSecret), or the one this chart
renders itself from .Values.secretValues (see templates/secret.yaml).
*/}}
{{- define "tender-acl.secretName" -}}
{{- default (printf "%s-secrets" (include "tender-acl.fullname" .)) .Values.existingSecret }}
{{- end }}
