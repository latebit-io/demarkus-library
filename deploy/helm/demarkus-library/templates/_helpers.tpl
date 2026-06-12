{{/*
Expand the name of the chart.
*/}}
{{- define "demarkus-library.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully qualified app name.
*/}}
{{- define "demarkus-library.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "demarkus-library.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "demarkus-library.labels" -}}
helm.sh/chart: {{ include "demarkus-library.chart" . }}
{{ include "demarkus-library.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "demarkus-library.selectorLabels" -}}
app.kubernetes.io/name: {{ include "demarkus-library.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "demarkus-library.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "demarkus-library.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Name of the chart-managed Secret holding the OAuth client secret
(cleartext mode only — existingSecretRef mode renders no Secret).
*/}}
{{- define "demarkus-library.secretName" -}}
{{- printf "%s-oauth" (include "demarkus-library.fullname" .) -}}
{{- end -}}

{{/*
Transport validation + cross-field requirements. Called from
deployment.yaml so every render path hits it. Mirrors NewAppConfig's
startup validation — catching the misconfig at template-render beats a
CrashLoop with the breadcrumb buried in pod logs.
*/}}
{{- define "demarkus-library.validate" -}}
{{- $t := .Values.library.transport -}}
{{- if and (ne $t "broker") (ne $t "quic") -}}
{{- fail (printf "library.transport must be \"broker\" or \"quic\", got %q" $t) -}}
{{- end -}}
{{- if eq $t "broker" -}}
{{- if not .Values.library.broker.url -}}
{{- fail "library.broker.url is required when library.transport is \"broker\"" -}}
{{- end -}}
{{- if not .Values.library.broker.clientID -}}
{{- fail "library.broker.clientID is required when library.transport is \"broker\"" -}}
{{- end -}}
{{- if not .Values.library.broker.redirectURI -}}
{{- fail "library.broker.redirectURI is required when library.transport is \"broker\"" -}}
{{- end -}}
{{- if not .Values.library.broker.world -}}
{{- fail "library.broker.world is required when library.transport is \"broker\"" -}}
{{- end -}}
{{- if and .Values.library.broker.clientSecret .Values.library.broker.existingSecretRef.name -}}
{{- fail "library.broker.clientSecret and library.broker.existingSecretRef.name are mutually exclusive; set exactly one" -}}
{{- end -}}
{{- if and (not .Values.library.broker.clientSecret) (not .Values.library.broker.existingSecretRef.name) -}}
{{- fail "library.broker.clientSecret or library.broker.existingSecretRef.name is required when library.transport is \"broker\"" -}}
{{- end -}}
{{- /* Catch this at render time rather than at pod startup: a blank
       existingSecretRef.key would render a secretKeyRef with no key,
       which the kubelet rejects with a CreateContainerConfigError
       only after the pod schedules. */ -}}
{{- if and .Values.library.broker.existingSecretRef.name (not .Values.library.broker.existingSecretRef.key) -}}
{{- fail "library.broker.existingSecretRef.key is required when library.broker.existingSecretRef.name is set" -}}
{{- end -}}
{{- else -}}
{{- if not .Values.library.quic.host -}}
{{- fail "library.quic.host is required when library.transport is \"quic\"" -}}
{{- end -}}
{{- end -}}
{{- end -}}
