# Changelog

## [0.0.2](https://github.com/sentinelfederalgroup/prismoid-agent1/compare/prismoid-v0.0.1...prismoid-v0.0.2) (2026-05-30)


### Features

* **auth:** in-app Twitch sign-in flow ([#78](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/78)) ([f52ac60](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/f52ac60f2e046446a2d6379a7de4c50550e1aba0))
* **badges:** resolve and render chat badges before display name ([#75](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/75)) ([cb3a911](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/cb3a911b9505b10af195c0259f6a26bfbaa8b57c))
* bake twitch client_id as compile-time const ([ffdba46](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/ffdba465abc2739a30a270b69191bfc050e75c5c))
* bench harness for ring drain + parse hot path ([de2e6b7](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/de2e6b7e23fcfd94d430a3b2832afe3602b1c27c))
* chatstore ring buffer with per-frame viewport signal ([#35](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/35)) ([c91963f](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/c91963f09eb07cc07fcd989e7839da1f3dd0039a))
* **chat:** timestamps and readable username colors ([#83](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/83)) ([3a525c0](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/3a525c0dbe7f3ede8ac7417ffe218a62837b7bfa))
* cross-platform badge and color normalization ([#85](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/85)) ([abba771](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/abba771ca44a9e005ea309a38b111652ea00dac6))
* **emotes:** scan messages against emote index on drain ([#72](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/72)) ([fc55a7c](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/fc55a7c5eb0aac26125c7d245c810d2c8f484d78))
* **emotes:** wire channel-join bundle from sidecar to host ([#69](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/69)) ([03a2a3d](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/03a2a3d52dc71f83b39765df3717996209cf7d7b))
* **frontend:** header chrome with live connection status ([#82](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/82)) ([67a800a](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/67a800a37388a829011fdaaa02feaf8de26cd471))
* **frontend:** render inline emotes in chat messages ([#73](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/73)) ([1348682](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/1348682490ca0021e376c3984750afe22a1e1bc9))
* **frontend:** virtualize chat feed with pretext ([#66](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/66)) ([946845a](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/946845a3ac3a1f2650561375e4c94def52e9f887))
* heartbeat payload ts + counter ([7c2d67b](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/7c2d67b0582b4b3d5d7e39bb4d5d8aeb7bbdeae5))
* **host:** lock-free emote index with aho-corasick scanner ([#68](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/68)) ([cb51f12](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/cb51f124f9ef559c2ae8f72a77fecc0710ab013b))
* **host:** respawn sidecar on heartbeat timeout ([#76](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/76)) ([e58c144](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/e58c14485ea277637c1621cedcca992690daddc5))
* kick chat read path via pusher websocket ([#80](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/80)) ([9eafe58](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/9eafe58f830d6b050d8e2b81fcbf6a7d11438bf5))
* live indicator, release profile, and opener cleanup ([#87](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/87)) ([d03d699](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/d03d6995bd06866496cf7817a0c281aa305bfa99))
* load .env.local for phase 0 dev creds ([#37](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/37)) ([b42e544](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/b42e544af84a36e54b153f392fc78205a17d33ec))
* mod action command dispatch (scaffold, no helix yet) ([ea2d396](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/ea2d396cc219b0973a309c38599598cf8423c464))
* optimistic message rendering with reconcile ([#94](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/94)) ([ea71c75](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/ea71c75c070663d73fb314c170a93fc3cc548d8d))
* proactive token refresh during sidecar session ([#92](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/92)) ([5b2682f](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/5b2682fb63c41c90183176accdb048a3e56d95c7))
* **ringbuf:** drop-oldest on full ring instead of dropping new writes ([#77](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/77)) ([48697a1](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/48697a174bf09bcfd9acf725664070c3f814c4b9))
* rust host lifecycle with unified message pipeline ([#33](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/33)) ([88bd4ac](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/88bd4ac9076df9a00e9f2acaaff8109d8179f7c2))
* send chat messages via helix ([#84](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/84)) ([4bb8911](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/4bb89115e9c9bf08cd61a16040e924e19b71b325))
* shared memory ring buffer (PRI-1) ([#21](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/21)) ([1959420](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/19594200ec9f11669e9590fd1d66733d0018db73))
* sidecar bootstrap, channel writer, twitch eventsub ([#32](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/32)) ([62db32e](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/62db32e967b91973de07d6b1cfc5c37f3d269f8f))
* sidecar respawn supervisor with exp backoff + childguard ([0c2b832](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/0c2b832d715ac569bb156c09e3dd7b3d6a09f9c0))
* **sidecar:** emote and badge provider fetchers ([#67](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/67)) ([78ad6f5](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/78ad6f5dc87c9cac0647099cffa81a2b8f399951))
* single-account twitch auth, auto-derive broadcaster from dcf ([9ef8293](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/9ef829327ac45308d5e849e83f0b6df3cd0e36b0))
* twitch helix http client with 429 retry ([fd4faca](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/fd4faca81f0cfce801b3017fc63ac1bb6f2026f6))
* twitch oauth library (dcf + keyring + refresh) ([e983ba7](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/e983ba784fcb4a1b4fe0c69a4b5cf00a2a549c1e))
* unified ordering foundation (effective_ts + arrival_seq) ([#93](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/93)) ([20a7e63](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/20a7e63e5de83623539212294e0f8aafd93137cb))
* website redesign + branding ([#25](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/25)) ([3868f71](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/3868f71dd33db19ed70c7dd1c9484a17a2065f0b))
* wire oauth authmanager into supervisor + dcf seed bin ([260211b](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/260211b86121ee63e74d9af8b6f3ea7272fd3fc8))
* youtube chat read path via grpc streamlist ([#81](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/81)) ([6eb9c0d](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/6eb9c0dd6cf42538e8a2ce0b4699882cef6c89e6))
* youtube oauth via pkce loopback ([#99](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/99)) ([38298e0](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/38298e012078a6540bd68a7d17762b2c47b404b5))
* youtube send message ([#103](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/103)) ([11a11cd](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/11a11cd95b2b47a4b275c581268e14a0715f330f))


### Bug Fixes

* address copilot review on pri-12 kernel event signal ([9f49dab](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/9f49dab5ae5a7adb239c3925ecaec22680d7ccf0))
* emotes, chat perf, and send UX ([#91](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/91)) ([71900b3](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/71900b3ff5db57c0cdd3fe7dec3452ef8d0d6151))
* identity guard, delete on refresh-invalid, trim dcf stdout ([69956e8](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/69956e851c6bae6b271ad31dfb79578ca5de95b6))
* redact tokens in debug impl, add classify helper tests ([76d07e0](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/76d07e07cc858dc98806fee78284797e050475e5))
* switch to opener plugin for external URLs ([#86](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/86)) ([5994a1a](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/5994a1a6a9549602ddc85df1d396e4f199466925))


### Performance

* **frontend:** hoist utf8 codec and skip redundant span sort ([#74](https://github.com/sentinelfederalgroup/prismoid-agent1/issues/74)) ([cee3547](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/cee35475722e3e3aab84501e07b8ffd32310ccbb))
* kernel event signal for sidecar to host ring wakeups ([a53702e](https://github.com/sentinelfederalgroup/prismoid-agent1/commit/a53702e941313a12b32d53aa0ab47e20aa23c663))
