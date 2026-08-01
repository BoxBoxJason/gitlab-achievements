{{/*
Expand the name of the chart.
*/}}
{{- define "gitlab-achievements.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name.
*/}}
{{- define "gitlab-achievements.fullname" -}}
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

{{- define "gitlab-achievements.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "gitlab-achievements.labels" -}}
helm.sh/chart: {{ include "gitlab-achievements.chart" . }}
{{ include "gitlab-achievements.selectorLabels" . }}
{{- with .Values.image.tag | default .Chart.AppVersion }}
app.kubernetes.io/version: {{ . | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "gitlab-achievements.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gitlab-achievements.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "gitlab-achievements.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "gitlab-achievements.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding the credentials: the one the operator manages, or
the one this chart creates.
*/}}
{{- define "gitlab-achievements.secretName" -}}
{{- default (include "gitlab-achievements.fullname" .) .Values.secrets.existingSecret }}
{{- end }}

{{/*
Port the container listens on. Fixed rather than configurable: the Service
maps whatever port it likes onto it, and the probes and PUBLIC_URL are what
anything outside the pod actually addresses.
*/}}
{{- define "gitlab-achievements.containerPort" -}}8080{{- end }}

{{/*
Non-secret configuration, shared by the Deployment and the backfill Job so the
two are always configured against the same instance. Credentials arrive
separately, through envFrom, so they never appear in a pod spec.
*/}}
{{- define "gitlab-achievements.env" -}}
- name: GITLAB_URL
  value: {{ required "config.gitlabUrl is required: the base URL of the GitLab instance to mirror" .Values.config.gitlabUrl | quote }}
- name: ACHIEVEMENTS_NAMESPACE
  value: {{ required "config.achievementsNamespace is required: the full path of the namespace that owns the achievement definitions" .Values.config.achievementsNamespace | quote }}
- name: PUBLIC_URL
  value: {{ required "config.publicUrl is required: GitLab's webhooks are registered against it" .Values.config.publicUrl | quote }}
- name: LISTEN_ADDR
  value: ":{{ include "gitlab-achievements.containerPort" . }}"
- name: LOG_LEVEL
  value: {{ .Values.config.logLevel | quote }}
- name: HOOK_SCOPE
  value: {{ .Values.config.hookScope | quote }}
- name: HOOK_RATE
  value: {{ .Values.config.hookRate | quote }}
- name: BACKFILL_SINCE
  value: {{ .Values.config.backfill.since | quote }}
- name: BACKFILL_RATE
  value: {{ .Values.config.backfill.rate | quote }}
- name: RECONCILE_INTERVAL
  value: {{ .Values.config.reconcile.interval | quote }}
- name: RECONCILE_LOOKBACK
  value: {{ .Values.config.reconcile.lookback | quote }}
- name: API_AUTH
  value: {{ .Values.config.api.auth | quote }}
- name: OAUTH_CLIENT_ID
  value: {{ .Values.config.api.oauthClientId | quote }}
{{- end }}

{{/*
Credentials, as an envFrom entry. Whether the Secret is the chart's or the
operator's, the key names are the app's environment variables.
*/}}
{{- define "gitlab-achievements.envFrom" -}}
- secretRef:
    name: {{ include "gitlab-achievements.secretName" . }}
{{- end }}
