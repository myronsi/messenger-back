# Maps

Location messages show a map image, and the location picker shows a map the user can move. Both come from our
server (`internal/maps`), so a viewer's browser never contacts the tile provider.

## Endpoints

| Endpoint | What |
|---|---|
| `GET /maps/tiles/{z}/{x}/{y}` | One 256 px PNG tile, zoom 0 to 19. Signed-in users, at most 600 per minute each (`429`). |
| `GET /maps/static?lat&lon&sig` | The 640 × 320 JPEG of a location message, zoom 16, pin in the middle. |

`sig` is an HMAC of the rounded coordinates with a key derived from `JWT_SECRET` (`maps.DeriveKey`), made when the
message is sent (`messages.Deps.Maps`). Without it the endpoint would be a free map renderer for anyone signed in.
A changed `JWT_SECRET` makes old map images answer `403`.

## Caching

- Tiles: `maps/tiles/{z}/{x}/{y}.png` in object storage, with `.json` next to it holding the upstream `ETag` and
  expiry. A tile is kept for what `Cache-Control` / `Expires` say, but at least 7 days. An expired tile is
  revalidated with `If-None-Match`; when the provider fails, the stale tile is served. Parallel requests for one
  tile make one upstream request (singleflight).
- Static images: `maps/static/v1/16/{lat}_{lon}.jpg`, made on the first request from the tiles and kept.
- Clients: tiles `private, max-age=86400`, static images `private, max-age=31536000, immutable`.

## OpenStreetMap tile policy

The default provider is `https://tile.openstreetmap.org/{z}/{x}/{y}.png`. Its usage policy
(https://operations.osmfoundation.org/policies/tiles/) asks, and this service does:

- a User-Agent that names the app and how to reach us (`MAP_TILE_USER_AGENT`; default
  `MessengerMaps/<version> (+<PUBLIC_WEB_ORIGIN>)`), and our site as `Referer`;
- honouring `Cache-Control`, `Expires` and `ETag`, at least 7 days when they say nothing;
- no prefetching, no bulk downloads, no offline maps: tiles are fetched only when a client asks for them;
- "© OpenStreetMap contributors" visible on every map (the clients draw it).

The policy "generally does not recommend" a caching proxy and may block heavy use without notice. Watch the
`503` rate of `/maps/*`. To switch to another provider (for example a paid plan of MapTiler or Stadia), set
`MAP_TILE_URL` to its template with `{z}`, `{x}` and `{y}` (and its key); no client changes are needed. Delete
`maps/` from the bucket when the new tiles look different.

## Configuration

| Variable | Default |
|---|---|
| `MAP_TILE_URL` | `https://tile.openstreetmap.org/{z}/{x}/{y}.png` |
| `MAP_TILE_USER_AGENT` | `MessengerMaps/<version> (+<PUBLIC_WEB_ORIGIN>)` |
