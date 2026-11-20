# Sharing links, public cards and mentions

Contract 2.1.0-alpha.41. What a shared link shows, who can join a group through one, and how `@mentions` work.

## Links

| Link on the web app | What it is | Logged out | Logged in |
|---|---|---|---|
| `/@name` | A user (their username) or a public group (its public name). One name space, unique ignoring case. | `GET /public/names/{name}` | `GET /usernames/{name}`, or `GET /public-groups/{name}` for a group |
| `/join/{token}` | A group invite link. The token is 22 random letters and digits. | `GET /public/invites/{token}` | `GET /invites/{token}`, then `POST /invites/{token}/join` |

## What people without an account see

`GET /public/names/{name}` answers `user`, `group` or `unavailable`. Unknown names, private users and hidden accounts all answer `unavailable`, so the endpoint does not tell whether a name exists.

A user card is shown only when all of these hold:

- `web_profile_visibility` is `everyone` (the default; new privacy setting),
- `search_visibility` is `everyone`,
- the account is not `restricted` or `suspended`.

The card has the display name and username, the avatar only when `avatar_visibility` is `everyone`, and the bio only when `profile_visibility` is `everyone`. It never has the ID, presence, last seen, birthday, pronouns, music or background.

A group card (public group or valid invite link) has the name, public name, description, avatar and member count. No members, no messages.

Every public route is rate limited per IP (60 a minute) and per /24 (or /48 for IPv6, 600 a minute) and answers `X-Robots-Tag: noindex, nofollow`.

## Link previews in other apps

`GET /og/names/{name}` and `GET /og/invites/{token}` return a small HTML page with Open Graph and Twitter tags built from the same cards, and `noindex`. Absolute links and images use `PUBLIC_WEB_ORIGIN`. The web server sends link-preview crawlers there and people to the web app:

```nginx
map $http_user_agent $is_preview_bot {
    default 0;
    ~*(TelegramBot|WhatsApp|Slackbot|facebookexternalhit|Twitterbot|Discordbot|LinkedInBot|SkypeUriPreview) 1;
}

location ~ ^/@([A-Za-z0-9_]{3,32})/?$ {
    add_header X-Robots-Tag "noindex, nofollow" always;
    if ($is_preview_bot) { rewrite ^/@(.*)$ /api/v2/og/names/$1 last; }
    try_files /index.html =404;
}

location ~ ^/join/([A-Za-z0-9]{22})/?$ {
    add_header X-Robots-Tag "noindex, nofollow" always;
    if ($is_preview_bot) { rewrite ^/join/(.*)$ /api/v2/og/invites/$1 last; }
    try_files /index.html =404;
}
```

## Groups

- Owner and admins create, change and revoke invite links (at most 50 active per group), and give the group a public name or take it away. Expiry (up to 366 days, or never) and use limit (1 to 100000, or none) are chosen per link.
- Joining by link or public name is direct, with a `member_joined` system message. At most 20 joins an hour per user.
- Removing a member bans them (`chat_bans`): they cannot rejoin by link or name and cannot be added until an admin lifts the ban. Leaving does not ban.
- Account status: `limited` and above cannot create links or public names (`share_group`); `restricted` and above cannot join by link (`join_by_link`).
- Reserved names (also refused at registration): see `internal/publicnames`.

## Mentions

- The server reads `@name` tokens from the text of text and file messages in groups: a token starts after a character that is not a letter, digit, `_` or `@`, and is 3 to 32 letters, digits or underscores. At most 20 distinct names.
- Names of current members become `Message.mentions`; other names stay plain text. `@all` from the owner or an admin sets `Message.mentions_all`.
- Each mentioned member (everyone but the sender for `@all`) gets a row in `unread_mentions`, unless the sender is `restricted` or above. Reading past the message, deleting it, or leaving the group removes the row. `Chat.unread_mentions_count` and `GET /chats/{chat_id}/mentions` read them.
- Pushes: a mention reaches the member even in a muted chat, unless their `mentions_in_muted` notification setting is off. The push payload has `kind: "mention"`. Editing recomputes the mentions but sends no push.
