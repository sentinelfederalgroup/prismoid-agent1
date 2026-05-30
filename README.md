# <picture><source srcset="assets/icon-white.svg" media="(prefers-color-scheme: dark)" /><img src="assets/icon.svg" alt="prismoid" width="20" /></picture> prismoid

unified live chat for streamers

[![website](https://img.shields.io/badge/website-prismoid.org-2ea043?style=for-the-badge&logo=googlechrome&logoColor=white)](https://prismoid.org)
[![download](https://img.shields.io/badge/download-latest%20release-2ea043?style=for-the-badge&logo=github&logoColor=white)](https://github.com/ImpulseB23/Prismoid/releases/latest)
[![contribute](https://img.shields.io/badge/contribute-open%20guide-238636?style=for-the-badge&logo=git&logoColor=white)](CONTRIBUTING.md)
[![docs](https://img.shields.io/badge/docs-architecture%20notes-1f6feb?style=for-the-badge&logo=readthedocs&logoColor=white)](docs/)

---

merges Twitch, YouTube, and Kick chat into a single window with cross-platform moderation and universal emote rendering.

- **one feed** from all platforms in a single stream
- **mod from one place** - ban, timeout, delete regardless of source platform
- **streamer agent** - local co-mod queue, four looks, streamer-only commands, approval-gated mod actions, chat pulse, and suggested replies
- **emotes everywhere** - 7TV, BTTV, FFZ render in all chats, including YouTube
- **OBS overlay** - browser source for clean unified chat on stream
- **no cloud backend** - talks directly to platform APIs from your machine

## stack

<table>
	<tbody>
		<tr>
			<td><strong>Rust</strong> (Tauri 2)</td>
			<td>desktop shell, message processing, emote scanning</td>
		</tr>
		<tr>
			<td><strong>Go</strong> (sidecar)</td>
			<td>network I/O, WebSocket connections, OAuth, platform APIs</td>
		</tr>
		<tr>
			<td><strong>TypeScript</strong> (SolidJS)</td>
			<td>frontend UI, virtual scrolling, emote rendering</td>
		</tr>
	</tbody>
</table>

## development

prerequisites: Rust toolchain, Go 1.26+, Node.js 20+, bun

```bash
cd apps/desktop
bun install
cargo tauri dev
```

## screenshots

> coming soon

## docs

see [`docs/`](docs/) for architecture, platform API details, performance requirements, and decision records.

## license

[GPL-3.0](LICENSE)
