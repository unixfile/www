(() => {
	const url = document.currentScript.dataset.live;
	let es = null;
	// A page goes down with its server: a stream lost for three seconds
	// reloads it, and the browser says the server is gone. A restart's new
	// server answers within a second or two and sends reload itself.
	let lost = null;
	const found = () => {
		clearTimeout(lost);
		lost = null;
	};
	const sync = () => {
		if (document.hidden) {
			if (es) es.close();
			es = null;
			found();
		} else if (!es) {
			es = new EventSource(url);
			es.onmessage = () => location.reload();
			es.onopen = found;
			es.onerror = () => {
				if (!lost) lost = setTimeout(() => location.reload(), 3000);
			};
		}
	};
	document.addEventListener("visibilitychange", sync);
	sync();

	// The build stamp counts the seconds since the binary was built. The
	// script runs in the head, so the stamp is found once the page is parsed.
	document.addEventListener("DOMContentLoaded", () => {
		const ago = document.querySelector("p.built[data-built] > span");
		if (!ago) return;
		const at = Number(ago.parentNode.dataset.built);
		const tick = () => {
			ago.textContent = Math.max(0, Math.floor((Date.now() - at) / 1000));
		};
		tick();
		setInterval(tick, 1000);
	});

	// A history's fold loads its commit's diff from the commit page the
	// first time it opens. toggle does not bubble, so it is caught on the
	// way down.
	document.addEventListener("toggle", (e) => {
		const d = e.target;
		if (!d.open || !d.dataset || !d.dataset.diff || d.dataset.loaded) return;
		d.dataset.loaded = "1";
		fetch(d.dataset.diff)
			.then((r) => r.text())
			.then((html) => {
				const got = new DOMParser().parseFromString(html, "text/html").querySelector("div.diffs");
				const slot = d.querySelector("div.diffs");
				if (got && slot) slot.replaceWith(document.adoptNode(got));
			})
			.catch(() => delete d.dataset.loaded);
	}, true);
})();
