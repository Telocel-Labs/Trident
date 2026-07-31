{{/*
Expand the name of the chart.
*/}}
{{- define "trident.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "trident.fullname" -}}
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
Chart label (name + version).
*/}}
{{- define "trident.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to every resource.
*/}}
{{- define "trident.labels" -}}
helm.sh/chart: {{ include "trident.chart" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Component-scoped selector labels.
Usage: {{ include "trident.selectorLabels" (dict "root" . "component" "go-api") }}
*/}}
{{- define "trident.selectorLabels" -}}
app.kubernetes.io/name: {{ include "trident.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{/*
Image pull secrets block.
*/}}
{{- define "trident.imagePullSecrets" -}}
{{- with .Values.global.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end }}

{{/*
Pod-level securityContext (issue #304): runs as the given non-root UID with
a restricted seccomp profile. Usage:
  {{ include "trident.podSecurityContext" (dict "uid" 1001) }}
*/}}
{{- define "trident.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: {{ .uid }}
runAsGroup: {{ .uid }}
fsGroup: {{ .uid }}
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{/*
Container-level securityContext (issue #304): read-only rootfs, no privilege
escalation, every Linux capability dropped. Same for every component — none
of them need any capability or a writable root filesystem (writable scratch
space, where needed, is mounted as an emptyDir at the call site).
*/}}
{{- define "trident.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end }}
