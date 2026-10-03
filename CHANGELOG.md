# Changelog

## [0.5.1](https://github.com/myronsi/messenger-back/compare/v0.5.0...v0.5.1) (2026-10-03)


### Features

* add GET /version endpoint ([#94](https://github.com/myronsi/messenger-back/issues/94)) ([3496ef2](https://github.com/myronsi/messenger-back/commit/3496ef27675a31c20aed821e80958ca1a0329b49))

## [0.5.0](https://github.com/myronsi/messenger-back/compare/v0.4.6...v0.5.0) (2026-10-03)


### Miscellaneous Chores

* release 0.5.0 ([#92](https://github.com/myronsi/messenger-back/issues/92)) ([ea4915e](https://github.com/myronsi/messenger-back/commit/ea4915e644ef12dfb0dfa0647a1d10621567cdc6))

## [0.4.6](https://github.com/myronsi/messenger-back/compare/v0.4.5...v0.4.6) (2026-10-03)


### Features

* **media:** store image dimensions and thumbnails for chat images ([12485f1](https://github.com/myronsi/messenger-back/commit/12485f199cd7db578dca81e5ee9881fb9092d123))
* **media:** store image dimensions and thumbnails for chat images ([ce55558](https://github.com/myronsi/messenger-back/commit/ce55558526e33904ba48df60e101c053e8916767))


### Bug Fixes

* track image_metadata module and its tests ([35e3c64](https://github.com/myronsi/messenger-back/commit/35e3c6429db145de644fc7e1f0881e04278f6399))

## [0.4.5](https://github.com/myronsi/messenger-back/compare/v0.4.4...v0.4.5) (2026-10-03)


### Bug Fixes

* **security:** authorize WebSocket events and authenticate with one-time tickets ([85b95b7](https://github.com/myronsi/messenger-back/commit/85b95b7e4225175dc48a09b9526a661807b07e9f))
* **security:** authorize WebSocket events and authenticate with one-time tickets ([886ce70](https://github.com/myronsi/messenger-back/commit/886ce703d4f44846995d11fc7c8291af4446c4c6))
* **security:** close sockets when a session ends and accept only own uploads over WS ([ede9e85](https://github.com/myronsi/messenger-back/commit/ede9e85c27dba59910892f1665d77a5a52544ea6))

## [0.4.4](https://github.com/myronsi/messenger-back/compare/v0.4.3...v0.4.4) (2026-10-02)


### Bug Fixes

* **auth:** harden password recovery and throttle auth endpoints ([00aec04](https://github.com/myronsi/messenger-back/commit/00aec042ead224458c4495ce8c5002b58637d5e1))
* **auth:** recover password with one user-held part ([3d86be0](https://github.com/myronsi/messenger-back/commit/3d86be06c430ebfe4d91dd35d621b9a42b076908))
* **auth:** validate usernames and stop using them as file paths ([d74e124](https://github.com/myronsi/messenger-back/commit/d74e124bddb6e2d07f785047b572017f1eb0ef57))
* **auth:** validate usernames and stop using them as file paths ([e75be0d](https://github.com/myronsi/messenger-back/commit/e75be0daa644132ffd5853ea993b3fcc100ef000))
* **security:** configure CORS and trusted hosts from the environment ([727ee05](https://github.com/myronsi/messenger-back/commit/727ee054bd61b4010c5bba57041647886da5a682))
* **security:** configure CORS and trusted hosts from the environment ([5ffc282](https://github.com/myronsi/messenger-back/commit/5ffc2829396845989169ab8370824a5f0c38b86e))
* **security:** validate uploads by content and re-encode avatars ([67341e5](https://github.com/myronsi/messenger-back/commit/67341e55321ccd8f276aed0bc195f7d1353c4be8))
* **security:** validate uploads by content and re-encode avatars ([f71a326](https://github.com/myronsi/messenger-back/commit/f71a32654df62389eb0b9e1a02b57c41086140f2))
* **users:** make username search case-insensitive ([1c39d3e](https://github.com/myronsi/messenger-back/commit/1c39d3eb00142d266773c87aacaeaf91d1c75129))
* **users:** make username search case-insensitive ([d10a0d1](https://github.com/myronsi/messenger-back/commit/d10a0d1cffe2431be7542aa330a9b139b830b732))
* **ws:** stop leaking chat-list events to non-members ([6844d6a](https://github.com/myronsi/messenger-back/commit/6844d6a26f2f195f2dcf70ea77d528a7087971f0))
* **ws:** stop leaking chat-list events to non-members ([f7ab522](https://github.com/myronsi/messenger-back/commit/f7ab522f7ae2564385b83862bc1600f7de9a58e6))

## [0.4.3](https://github.com/myronsi/messenger-back/compare/v0.4.2...v0.4.3) (2026-10-02)


### Bug Fixes

* ship valid built-in avatars (default, group, deleted) ([9ad112e](https://github.com/myronsi/messenger-back/commit/9ad112e9c15c2de85026dcbf6360a88c44838457))
* ship valid built-in avatars (default, group, deleted) ([f0e4849](https://github.com/myronsi/messenger-back/commit/f0e48491ff637eb32d7876c2c214e7335e970c88))
* use proper silhouette artwork for built-in avatars ([f609c09](https://github.com/myronsi/messenger-back/commit/f609c094c9a22de7aa6eff483f4b1b18f3a60352))
* use proper silhouette artwork for built-in avatars ([5a36844](https://github.com/myronsi/messenger-back/commit/5a36844f3756c7d4e65b2e44c61b722e1723a909))

## [0.4.2](https://github.com/myronsi/messenger-back/compare/v0.4.1...v0.4.2) (2026-10-02)


### Bug Fixes

* scope session cookies to the proxy path so media and refresh work behind /api ([554dcfe](https://github.com/myronsi/messenger-back/commit/554dcfe454f716a159299fbfe084676ac8ac363a))
* scope session cookies to the proxy path so media and refresh work behind /api ([e07c146](https://github.com/myronsi/messenger-back/commit/e07c1465a56ef4cf2532eddc7a8f916fd467708f))

## [0.4.1](https://github.com/myronsi/messenger-back/compare/v0.4.0...v0.4.1) (2026-10-02)


### Bug Fixes

* **MSGC-36:** drop the legacy placeholder key and TOTP migration ([28f30fb](https://github.com/myronsi/messenger-back/commit/28f30fb6d0a8d77994d4c74804330db6875bccc1))
* **MSGC-36:** load JWT secret from env and harden recovery and 2FA tokens ([3926e6d](https://github.com/myronsi/messenger-back/commit/3926e6d0ba8d1d621b92f3196486950316b6ab85))
* **MSGC-36:** load JWT secret from env and harden recovery and 2FA tokens ([3866f36](https://github.com/myronsi/messenger-back/commit/3866f36aa46780a9e255ec8ed978be7c7fa89ae8))
* **MSGC-37:** deliver chat-list and presence events only to chat members ([42c02ac](https://github.com/myronsi/messenger-back/commit/42c02ac9fa41daad5eb5a9892e5e2d6cb30adaa3))
* **MSGC-37:** deliver chat-list and presence events only to chat members ([46fb809](https://github.com/myronsi/messenger-back/commit/46fb809bd580cfe95978361d677e51140f2a5592))
* **MSGC-38:** stop logging recovery secrets and message contents ([ab2473f](https://github.com/myronsi/messenger-back/commit/ab2473fdbb683213fcd26acc800bd2df76dc57f0))
* **MSGC-38:** stop logging recovery secrets and message contents ([b23ec3b](https://github.com/myronsi/messenger-back/commit/b23ec3ba81dc1c693ab6e7dc21b9d0c8efc901d3))
* **MSGC-40:** add authenticated media route ([e1ef8d5](https://github.com/myronsi/messenger-back/commit/e1ef8d51166fb670284dcb1fe4f25a881ae1cbb9))
* **MSGC-40:** serve uploads and avatars only to authorised users ([dba30e0](https://github.com/myronsi/messenger-back/commit/dba30e06128b7d494300d0e19aa7d44d47a963e8))
* **MSGC-40:** serve uploads and avatars only to authorised users ([a3f5531](https://github.com/myronsi/messenger-back/commit/a3f5531704a8fec7462bf74e25e0a61bd306849d))

## 0.4.0 – baseline

Baseline before versioning: the state of `master` when Semantic Versioning was adopted. Later entries are generated by release-please (MSGC-58).
