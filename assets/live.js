(() => {
	let url = document.currentScript.dataset.live;
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
			es.onmessage = update;
			es.onopen = found;
			es.onerror = () => {
				if (!lost) lost = setTimeout(reload, 3000);
			};
		}
	};

	// A reload keeps the listing entry that had focus, so arrows go on
	// from where they were.
	const focusKey = "focus";
	const reload = () => {
		const a = document.activeElement;
		try {
			if (a && a.matches("ul.listing a")) sessionStorage.setItem(focusKey, a.getAttribute("href"));
		} catch {}
		location.reload();
	};
	document.addEventListener("DOMContentLoaded", () => {
		let href = null;
		try {
			href = sessionStorage.getItem(focusKey);
			sessionStorage.removeItem(focusKey);
		} catch {}
		const a = [...document.querySelectorAll("ul.listing a")].find((a) => a.getAttribute("href") === href);
		if (a) a.focus();
	});

	// A doc takes its new version in place, and each block that differs
	// from the one shown before fills with the target colour and fades.
	// Any other page, a page from a new stylesheet, or a doc that is gone
	// reloads whole.
	const blockKey = (el) => {
		if (!el.classList.contains("changed")) return el.outerHTML;
		const c = el.cloneNode(true);
		c.classList.remove("changed");
		if (!c.classList.length) c.removeAttribute("class");
		return c.outerHTML;
	};
	const styleOf = (doc) => {
		const l = doc.querySelector('link[rel="stylesheet"]');
		return l && l.getAttribute("href");
	};
	const update = () => {
		if (document.body.className !== "doc") return reload();
		fetch(location.href, { cache: "no-store" })
			.then((r) => (r.ok ? r.text() : Promise.reject()))
			.then((html) => {
				const next = new DOMParser().parseFromString(html, "text/html");
				const live = next.querySelector("script[data-live]");
				const main = next.querySelector("main");
				if (!live || !main || next.body.className !== "doc" || styleOf(next) !== styleOf(document)) return reload();
				const shown = new Map();
				for (const el of document.querySelectorAll("main > *")) {
					const k = blockKey(el);
					shown.set(k, (shown.get(k) || 0) + 1);
				}
				for (const el of main.children) {
					const k = el.outerHTML;
					if (shown.get(k)) shown.set(k, shown.get(k) - 1);
					else el.classList.add("changed");
				}
				const x = scrollX, y = scrollY;
				document.title = next.title;
				document.body.replaceWith(document.adoptNode(next.body));
				scrollTo(x, y);
				url = live.dataset.live;
				if (es) es.close();
				es = null;
				sync();
			})
			.catch(reload);
	};
	document.addEventListener("visibilitychange", sync);
	sync();

	// The build stamp counts the seconds since the binary was built. It is
	// looked up on each tick, since a doc's update replaces it.
	setInterval(() => {
		const ago = document.querySelector("p.built[data-built] > span");
		if (ago) ago.textContent = Math.max(0, Math.floor((Date.now() - Number(ago.parentNode.dataset.built)) / 1000));
	}, 1000);

	// Arrows walk a listing: Up and Down move between entries, Right or
	// Space opens one, as Enter does, and Left goes up a directory. Other
	// pages keep their arrows for scrolling.
	document.addEventListener("keydown", (e) => {
		if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || e.defaultPrevented) return;
		if (e.target.closest && e.target.closest("input, textarea, select, [contenteditable]")) return;
		const links = [...document.querySelectorAll("ul.listing a")];
		if (!links.length) return;
		const i = links.indexOf(document.activeElement);
		switch (e.key) {
			case "ArrowDown":
				links[Math.min(i + 1, links.length - 1)].focus();
				break;
			case "ArrowUp":
				links[i < 0 ? links.length - 1 : Math.max(i - 1, 0)].focus();
				break;
			case "ArrowRight":
			case " ":
				if (i < 0) return;
				location.href = links[i].href;
				break;
			case "ArrowLeft": {
				const up = links.find((a) => a.getAttribute("href") === "../");
				if (!up) return;
				location.href = up.href;
				break;
			}
			default:
				return;
		}
		e.preventDefault();
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
