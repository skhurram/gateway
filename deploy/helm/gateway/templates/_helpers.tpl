{{/* Chart name, truncated to the 63 character DNS label limit. */}}
{{- define "gateway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Release-qualified name used for every resource. */}}
{{- define "gateway.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gateway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "gateway.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "gateway.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "gateway.controllerName" -}}
{{- printf "%s-controller" (include "gateway.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "gateway.controllerLabels" -}}
app.kubernetes.io/name: {{ include "gateway.name" . }}-controller
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "gateway.configMapName" -}}
{{- default (include "gateway.fullname" .) .Values.controller.configMapName -}}
{{- end -}}

{{/* Container port, taken from the port in config.listen (":8080" -> 8080). */}}
{{- define "gateway.port" -}}
{{- regexFind "[0-9]+$" .Values.config.listen | int -}}
{{- end -}}
