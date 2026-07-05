{{- define "kscribe.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kscribe.fullname" -}}
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

{{- define "kscribe.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/name: {{ include "kscribe.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "kscribe.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kscribe.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "kscribe.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "kscribe.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "kscribe.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{/* Name of the Secret holding the LLM API key. */}}
{{- define "kscribe.llmSecretName" -}}
{{- if .Values.llm.existingSecret -}}
{{- .Values.llm.existingSecret -}}
{{- else -}}
{{- printf "%s-llm" (include "kscribe.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "kscribe.llmSecretKey" -}}
{{- if .Values.llm.existingSecret -}}
{{- .Values.llm.existingSecretKey -}}
{{- else -}}
api-key
{{- end -}}
{{- end -}}

{{/* Name of the Secret holding the dashboard auth token. */}}
{{- define "kscribe.dashboardSecretName" -}}
{{- if .Values.dashboard.existingSecret -}}
{{- .Values.dashboard.existingSecret -}}
{{- else -}}
{{- printf "%s-dashboard" (include "kscribe.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "kscribe.dashboardSecretKey" -}}
{{- if .Values.dashboard.existingSecret -}}
{{- .Values.dashboard.existingSecretKey -}}
{{- else -}}
dashboard-token
{{- end -}}
{{- end -}}

{{/* Name of the Secret holding the Resend API key. */}}
{{- define "kscribe.resendSecretName" -}}
{{- if .Values.notifications.resend.existingSecret -}}
{{- .Values.notifications.resend.existingSecret -}}
{{- else -}}
{{- printf "%s-resend" (include "kscribe.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "kscribe.resendSecretKey" -}}
{{- if .Values.notifications.resend.existingSecret -}}
{{- .Values.notifications.resend.existingSecretKey -}}
{{- else -}}
resend-api-key
{{- end -}}
{{- end -}}

{{/* Name of the Secret holding the Slack webhook URL. */}}
{{- define "kscribe.slackSecretName" -}}
{{- if .Values.notifications.slack.existingSecret -}}
{{- .Values.notifications.slack.existingSecret -}}
{{- else -}}
{{- printf "%s-slack" (include "kscribe.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "kscribe.slackSecretKey" -}}
{{- if .Values.notifications.slack.existingSecret -}}
{{- .Values.notifications.slack.existingSecretKey -}}
{{- else -}}
slack-webhook-url
{{- end -}}
{{- end -}}
