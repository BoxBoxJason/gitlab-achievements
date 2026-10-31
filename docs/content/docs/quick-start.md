---
title: Quick start
description: "Get GitLab Achievements running in minutes with Docker Compose on a self-hosted GitLab 16.6 or later, from tokens to the first awarded badge."
weight: 15
---

You need a self-hosted GitLab 16.6 or later, a group to hold the achievement definitions, two access tokens and a URL your instance can reach. [GitLab-side setup]({{< relref "/docs/gitlab-setup" >}}) covers all of it and takes about ten minutes.

Then, on any host with Docker Compose:

```bash
git clone https://github.com/BoxBoxJason/gitlab-achievements.git
cd gitlab-achievements
cp .env.example .env
$EDITOR .env
docker compose up -d
```

The first start creates 330 achievements and registers webhooks before it serves anything, so give it a minute (longer on Free/CE, where every project gets its own hook). Once `/readyz` answers, you are live:

```bash
curl -sf http://127.0.0.1:8080/readyz && echo ready
```

Awards start going out as people work, and history gets walked in the background so past activity counts too. Open the same URL in a browser for the [leaderboard and everyone's progress]({{< relref "/docs/frontend" >}}).

{{< callout type="warning" >}}
Warn your team: GitLab keeps an award hidden until its recipient accepts it from the email it sends, and [nobody but the recipient can do that]({{< relref "/docs/achievements-api-behavior" >}}).
{{< /callout >}}

For anything beyond a single host, see [Deployment]({{< relref "/docs/deployment" >}}).
