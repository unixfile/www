(() => {
	const url = document.currentScript.dataset.live;
	let es = null;
	const sync = () => {
		if (document.hidden) {
			if (es) es.close();
			es = null;
		} else if (!es) {
			es = new EventSource(url);
			es.onmessage = () => location.reload();
		}
	};
	document.addEventListener("visibilitychange", sync);
	sync();

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
