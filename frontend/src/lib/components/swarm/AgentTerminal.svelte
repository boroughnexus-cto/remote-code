<script lang="ts">
	import { onMount, onDestroy } from 'svelte';

	interface Props {
		tmuxSession: string;
	}

	let { tmuxSession }: Props = $props();

	let containerEl: HTMLDivElement;
	let term: any = null;
	let fitAddon: any = null;
	let canvasAddon: any = null;
	let ws: WebSocket | null = null;
	let resizeObserver: ResizeObserver | null = null;
	let resizeTimeout: ReturnType<typeof setTimeout> | null = null;

	// Module-level promise so concurrent mounts share one load cycle.
	// Assigned BEFORE the async work begins (prevents TOCTOU).
	let xtermLoadPromise: Promise<void> | null = null;

	function ensureXterm(): Promise<void> {
		if (typeof window === 'undefined') return Promise.resolve();
		if ((window as any).Terminal) return Promise.resolve();
		if (xtermLoadPromise) return xtermLoadPromise;

		xtermLoadPromise = new Promise<void>((resolve) => {
			const srcs = [
				'https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js',
				'https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js',
				'https://cdn.jsdelivr.net/npm/xterm-addon-canvas@0.5.0/lib/xterm-addon-canvas.js',
				'https://cdn.jsdelivr.net/npm/@xterm/addon-unicode11@0.8.0/lib/addon-unicode11.js',
			];
			function loadNext(i: number) {
				if (i >= srcs.length) { resolve(); return; }
				const s = document.createElement('script');
				s.src = srcs[i];
				s.onload = () => loadNext(i + 1);
				s.onerror = () => loadNext(i + 1);
				document.head.appendChild(s);
			}
			// Inject xterm CSS once
			if (!document.querySelector('link[href*="xterm@5.3.0"]')) {
				const link = document.createElement('link');
				link.rel = 'stylesheet';
				link.href = 'https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css';
				document.head.appendChild(link);
			}
			loadNext(0);
		});
		return xtermLoadPromise;
	}

	function createTerminal() {
		if (!containerEl) return;
		const W = window as any;
		if (!W.Terminal) return;

		term = new W.Terminal({
			cursorBlink: true,
			fontSize: 13,
			fontFamily: 'Monaco, Menlo, "Ubuntu Mono", monospace',
			lineHeight: 1.0,
			letterSpacing: 0,
			allowTransparency: false,
			allowProposedApi: true,
			disableStdin: true, // read-only
		});

		if (W.FitAddon) {
			fitAddon = new W.FitAddon.FitAddon();
			term.loadAddon(fitAddon);
		}
		if (W.CanvasAddon) {
			canvasAddon = new W.CanvasAddon.CanvasAddon();
			term.loadAddon(canvasAddon);
		}
		if (W.Unicode11Addon) {
			const u11 = new W.Unicode11Addon.Unicode11Addon();
			term.loadAddon(u11);
			term.unicode.activeVersion = '11';
		}

		term.open(containerEl);
		fitAddon?.fit();

		// WebSocket
		const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
		ws = new WebSocket(`${proto}//${location.host}/ws?session=${tmuxSession}`);
		ws.binaryType = 'arraybuffer';

		const decoder = new TextDecoder('utf-8');
		ws.onopen = () => {
			if (term && fitAddon) {
				ws?.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
			}
		};
		ws.onmessage = (ev) => {
			if (!term) return;
			if (typeof ev.data === 'string') {
				term.write(ev.data);
			} else {
				const text = decoder.decode(new Uint8Array(ev.data), { stream: true });
				if (text) term.write(text);
			}
		};
		ws.onerror = () => {};
		ws.onclose = () => {};

		// ResizeObserver for container size changes
		resizeObserver = new ResizeObserver(() => {
			if (resizeTimeout) clearTimeout(resizeTimeout);
			resizeTimeout = setTimeout(() => {
				fitAddon?.fit();
				if (ws?.readyState === WebSocket.OPEN && term) {
					ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
				}
			}, 50);
		});
		resizeObserver.observe(containerEl);
	}

	function destroyTerminal() {
		if (resizeObserver) {
			resizeObserver.disconnect();
			resizeObserver = null;
		}
		if (resizeTimeout) {
			clearTimeout(resizeTimeout);
			resizeTimeout = null;
		}
		if (ws) {
			ws.close();
			ws = null;
		}
		try {
			if (canvasAddon) { canvasAddon.dispose(); canvasAddon = null; }
			if (fitAddon) { fitAddon.dispose(); fitAddon = null; }
			if (term) { term.dispose(); term = null; }
		} catch (e) {
			// ignore disposal errors
		}
	}

	onMount(() => {
		ensureXterm().then(createTerminal);
	});

	onDestroy(() => {
		destroyTerminal();
	});
</script>

<div
	bind:this={containerEl}
	class="w-full h-40 bg-black rounded-lg overflow-hidden border border-slate-700/50"
></div>
