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
{{- with include "gitlab-achievements.imageTag" . }}
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
Container image reference, shared by every workload so they always run the
same build. image.tag may carry a digest (0.3.0@sha256:...), which is passed
through as is: repository:tag@digest is a valid reference.
*/}}
{{- define "gitlab-achievements.image" -}}
{{- printf "%s:%s" .Values.image.repository (include "gitlab-achievements.imageRef" .) -}}
{{- end }}

{{/*
image.tag as written, digest included; falls back to the chart's appVersion.
*/}}
{{- define "gitlab-achievements.imageRef" -}}
{{- .Values.image.tag | default .Chart.AppVersion | toString -}}
{{- end }}

{{/*
Image tag without any @digest, for the app.kubernetes.io/version label, which
cannot contain "@" or ":".
*/}}
{{- define "gitlab-achievements.imageTag" -}}
{{- include "gitlab-achievements.imageRef" . | splitList "@" | first -}}
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
separately, so they never appear in a pod spec.
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
  {{- with .Values.config.api.oauthClientIdRef }}
  {{- if .name }}
  valueFrom:
    secretKeyRef:
      name: {{ .name | quote }}
      key: {{ required "config.api.oauthClientIdRef.key is required alongside name" .key | quote }}
  {{- else }}
  value: {{ $.Values.config.api.oauthClientId | quote }}
  {{- end }}
  {{- else }}
  value: {{ $.Values.config.api.oauthClientId | quote }}
  {{- end }}
- name: API_ALLOWED_GROUP
  value: {{ .Values.config.api.allowedGroup | quote }}
- name: API_AUTH_CACHE_TTL
  value: {{ .Values.config.api.authCacheTTL | quote }}
- name: SESSION_LIFETIME
  value: {{ .Values.config.api.sessionLifetime | quote }}
- name: OAUTH_RETURN_ORIGINS
  value: {{ join "," .Values.config.api.returnOrigins | quote }}
{{- with include "gitlab-achievements.defaultDsn" . }}
- name: DATABASE_DSN
  value: {{ . | quote }}
{{- end }}
- name: EXCLUDE_BOTS
  value: {{ .Values.config.exclusions.bots | quote }}
- name: EXCLUDE_USERS
  value: {{ include "gitlab-achievements.excludeUsers" . | quote }}
- name: EXCLUDE_USERNAME_PATTERN
  value: {{ .Values.config.exclusions.usernamePattern | quote }}
- name: ALLOW_OPT_OUT
  value: {{ .Values.config.exclusions.allowOptOut | quote }}
{{- end }}

{{/*
Credentials taken from a Secret key of the operator's choosing, as env
entries. An env entry beats envFrom, so a ref overrides existingSecret.
*/}}
{{- define "gitlab-achievements.credentialEnv" -}}
{{- $refs := .Values.secrets.refs | default dict -}}
{{- range $name, $env := dict "gitlabReadToken" "GITLAB_READ_TOKEN" "gitlabWriteToken" "GITLAB_WRITE_TOKEN" "webhookSecret" "WEBHOOK_SECRET" "databaseDsn" "DATABASE_DSN" "oauthClientSecret" "OAUTH_CLIENT_SECRET" "sessionSecret" "SESSION_SECRET" }}
{{- $ref := get $refs $name }}
{{- if and $ref $ref.name }}
- name: {{ $env }}
  valueFrom:
    secretKeyRef:
      name: {{ $ref.name | quote }}
      key: {{ required (printf "secrets.refs.%s needs a key" $name) $ref.key | quote }}
{{- end }}
{{- end }}
{{- end }}

{{/* Credentials from existingSecret, as an envFrom entry. */}}
{{- define "gitlab-achievements.envFrom" -}}
{{- with .Values.secrets.existingSecret }}
- secretRef:
    name: {{ . }}
{{- end }}
{{- end }}

{{/*
Fails unless every required credential comes from a ref or existingSecret.
databaseDsn may instead be left unset to use SQLite on the PVC.
*/}}
{{- define "gitlab-achievements.validateSecrets" -}}
{{- $refs := .Values.secrets.refs | default dict -}}
{{- if not .Values.secrets.existingSecret }}
{{- range $name := list "gitlabReadToken" "gitlabWriteToken" "webhookSecret" }}
{{- if not (get (get $refs $name | default dict) "name") }}
{{- fail (printf "secrets.refs.%s or secrets.existingSecret is required" $name) }}
{{- end }}
{{- end }}
{{- if and (eq $.Values.config.api.auth "gitlab") (not (get (get $refs "sessionSecret" | default dict) "name")) }}
{{- fail "secrets.refs.sessionSecret or secrets.existingSecret is required when config.api.auth is gitlab" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
SQLite file on the PVC, used as DATABASE_DSN when nothing else provides one.
*/}}
{{- define "gitlab-achievements.defaultDsn" -}}
{{- $ref := get (.Values.secrets.refs | default dict) "databaseDsn" | default dict -}}
{{- if and (not .Values.secrets.existingSecret) (not $ref.name) -}}
{{- if not .Values.persistence.enabled -}}
{{- fail "secrets.refs.databaseDsn or secrets.existingSecret is required when persistence.enabled is false" -}}
{{- end -}}
{{- printf "sqlite://%s/gitlab-achievements.db" (trimSuffix "/" .Values.persistence.mountPath) -}}
{{- end -}}
{{- end }}

{{/*
Name of the PersistentVolumeClaim mounted for the default SQLite database:
one the operator already has, or the one this chart creates.
*/}}
{{- define "gitlab-achievements.pvcName" -}}
{{- default (printf "%s-data" (include "gitlab-achievements.fullname" .)) .Values.persistence.existingClaim }}
{{- end }}

{{/*
The data volume, shared by the Deployment and both Jobs so all three see the
same SQLite file. Empty when persistence is disabled, e.g. because
the database DSN points elsewhere.
*/}}
{{- define "gitlab-achievements.volumes" -}}
{{- if .Values.persistence.enabled -}}
- name: data
  persistentVolumeClaim:
    claimName: {{ include "gitlab-achievements.pvcName" . }}
{{- end }}
{{- end }}

{{- define "gitlab-achievements.volumeMounts" -}}
{{- if .Values.persistence.enabled -}}
- name: data
  mountPath: {{ .Values.persistence.mountPath }}
{{- end }}
{{- end }}

{{/*
config.exclusions.users, comma-joined. An unquoted user ID reaches the
template as a float64, which join would render as 1.234567e+06 from a
million up, silently matching nobody, so whole numbers are printed as
integers first.
*/}}
{{- define "gitlab-achievements.excludeUsers" -}}
{{- $users := list -}}
{{- range .Values.config.exclusions.users -}}
{{- if kindIs "float64" . -}}
{{- $users = append $users (int64 . | toString) -}}
{{- else -}}
{{- $users = append $users (toString .) -}}
{{- end -}}
{{- end -}}
{{- join "," $users -}}
{{- end }}
