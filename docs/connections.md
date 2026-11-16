# Connected accounts

Users can show verified accounts of other services on their profile: name and link only. OAuth tokens are used once
to read the account and are never stored. At most 15 accounts per user; they are visible like the bio
(`profile_visibility`).

Two kinds of providers:

- **Sign-in providers** (GitHub, Spotify, Steam, Xbox, Discord, Twitch, YouTube, Reddit, Roblox, TikTok). The user is sent to
  the provider and comes back to `GET {CONNECTIONS_CALLBACK_URL}/connections/callback/{provider}`. The one-time `state`
  (Redis, 10 minutes) names the user; PKCE (S256) protects the code. The browser then lands on
  `{CONNECTIONS_WEB_URL}/settings/connections?provider=…&result=connected|denied|failed|limit`.
- **Code providers** (Bluesky, Mastodon, website). `claim` returns `messenger-verify=<16 characters>`, valid for an
  hour; the user publishes it and calls `verify`. Bluesky: profile description, read from `public.api.bsky.app`.
  Mastodon: bio or a profile field, read from `https://<instance>/api/v1/accounts/lookup`. Website: a DNS TXT record on
  the domain, or `<meta name="messenger-verification" content="…">` on `https://<domain>/`. Instance and website fetches
  use the link-preview client, which only connects to public addresses on ports 80 and 443. They are on by default
  (`CONNECTIONS_VERIFIERS_ENABLED=false` turns them off).

`GET /meta` lists the providers the server offers (`connection_providers`).

## Setup

Set the two base URLs once:

| Variable | Example |
| --- | --- |
| `CONNECTIONS_CALLBACK_URL` | `https://messenger.example.com/api/v2` (the public API base) |
| `CONNECTIONS_WEB_URL` | `https://messenger.example.com` (the web app) |

Each sign-in provider is enabled only when both of its variables are set. Register one app per provider and use
`{CONNECTIONS_CALLBACK_URL}/connections/callback/<provider>` as its redirect URL, for example
`https://messenger.example.com/api/v2/connections/callback/github`.

| Provider | Where to register | Scopes | Variables |
| --- | --- | --- | --- |
| GitHub | github.com → Settings → Developer settings → OAuth Apps | `read:user` | `CONNECT_GITHUB_CLIENT_ID`, `CONNECT_GITHUB_CLIENT_SECRET` |
| Spotify | developer.spotify.com/dashboard (Web API) | none | `CONNECT_SPOTIFY_CLIENT_ID`, `CONNECT_SPOTIFY_CLIENT_SECRET` |
| Xbox | portal.azure.com → App registrations, "Personal Microsoft accounts only", a client secret | `XboxLive.signin` | `CONNECT_XBOX_CLIENT_ID`, `CONNECT_XBOX_CLIENT_SECRET` |
| Discord | discord.com/developers/applications → OAuth2 | `identify` | `CONNECT_DISCORD_CLIENT_ID`, `CONNECT_DISCORD_CLIENT_SECRET` |
| Twitch | dev.twitch.tv/console/apps (confidential client) | none | `CONNECT_TWITCH_CLIENT_ID`, `CONNECT_TWITCH_CLIENT_SECRET` |
| YouTube | console.cloud.google.com → APIs: enable YouTube Data API v3, OAuth client "Web application", consent screen | `youtube.readonly` | `CONNECT_YOUTUBE_CLIENT_ID`, `CONNECT_YOUTUBE_CLIENT_SECRET` |
| Reddit | reddit.com/prefs/apps → "web app" | `identity` | `CONNECT_REDDIT_CLIENT_ID`, `CONNECT_REDDIT_CLIENT_SECRET` |
| Roblox | create.roblox.com → Credentials → OAuth 2.0 apps | `openid`, `profile` | `CONNECT_ROBLOX_CLIENT_ID`, `CONNECT_ROBLOX_CLIENT_SECRET` |
| TikTok | developers.tiktok.com → Manage apps → Login Kit (web), redirect URI as above; the app needs review before others can use it | `user.info.basic`, `user.info.profile` | `CONNECT_TIKTOK_CLIENT_KEY`, `CONNECT_TIKTOK_CLIENT_SECRET` |
| Steam | steamcommunity.com/dev/apikey (Web API key; the domain is the web app's) | OpenID 2.0, no app | `CONNECT_STEAM_API_KEY` |

Notes:

- Google shows an "unverified app" screen for `youtube.readonly` until the consent screen is verified.
- Microsoft: an Xbox gamertag is read through Xbox Live (`user.auth.xboxlive.com`, `xsts.auth.xboxlive.com`); accounts
  without an Xbox profile fail with `result=failed`.
- Never commit the secrets; put them in the server's `.env` (deploy/go/env.example lists the names).
