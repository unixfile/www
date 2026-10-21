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
			es.onmessage = () => (searching() ? (pending = true) : update());
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

	// The box under the header searches the page's directory as the user
	// types: names first, then lines. The results stand in for the page
	// while the box holds a query; Escape or an empty box brings the page
	// back, with any update that arrived meanwhile. The query rides in the
	// URL as ?q=, so Back from a result and a reload show the results again.
	let pending = false;
	let timer = null;
	let ctl = null;
	const box = () => document.querySelector("input.search");
	const searching = () => {
		const b = box();
		return !!b && b.value.trim() !== "";
	};
	const showPage = () => {
		const ul = document.querySelector("ul.found");
		if (ul) ul.remove();
		const main = document.querySelector("main");
		if (main) main.hidden = false;
		if (pending) {
			pending = false;
			update();
		}
	};
	// withQuery is this page's URL holding q, or none; other parameters
	// keep their spelling, so ?log stays ?log.
	const withQuery = (q) => {
		const rest = location.search.slice(1).split("&").filter((s) => s && s !== "q" && !s.startsWith("q="));
		if (q) rest.push("q=" + encodeURIComponent(q));
		return location.pathname + (rest.length ? "?" + rest.join("&") : "") + location.hash;
	};
	const runSearch = () => {
		const b = box();
		const q = b.value.trim();
		const u = withQuery(q);
		if (u !== location.pathname + location.search + location.hash) history.replaceState(history.state, "", u);
		if (ctl) ctl.abort();
		if (!q) return showPage();
		ctl = new AbortController();
		fetch(b.dataset.search + "?search=" + encodeURIComponent(q), { signal: ctl.signal })
			.then((r) => r.json())
			.then((res) => {
				if (!box() || box().value.trim() !== q) return;
				const ul = document.createElement("ul");
				ul.className = "listing found";
				const add = (h, label, text) => {
					const li = document.createElement("li");
					const a = document.createElement("a");
					a.href = h.href;
					a.textContent = label;
					li.append(a);
					if (text) {
						const span = document.createElement("span");
						span.className = "line";
						span.textContent = text;
						li.append(" ", span);
					}
					ul.append(li);
				};
				for (const h of res.names) add(h, h.path);
				for (const h of res.lines) add(h, h.path + ":" + h.line, h.text);
				const note = document.createElement("li");
				note.className = "note";
				note.textContent = (res.names.length + res.lines.length ? "" : "nothing found, ") +
					"searched " + res.files + " files" + (res.cut ? ", stopped early" : "");
				ul.append(note);
				const main = document.querySelector("main");
				const old = document.querySelector("ul.found");
				if (old) old.replaceWith(ul);
				else main.before(ul);
				main.hidden = true;
			})
			.catch(() => {});
	};
	const clearSearch = () => {
		const b = box();
		b.value = "";
		b.blur();
		runSearch();
	};
	document.addEventListener("DOMContentLoaded", () => {
		const q = new URLSearchParams(location.search).get("q");
		if (!q || !box()) return;
		box().value = q;
		runSearch();
	});
	document.addEventListener("input", (e) => {
		if (!e.target.matches("input.search")) return;
		clearTimeout(timer);
		timer = setTimeout(runSearch, 120);
	});
	document.addEventListener("keydown", (e) => {
		if (!e.target.matches || !e.target.matches("input.search")) return;
		const first = document.querySelector("ul.found a");
		if (e.key === "Escape") clearSearch();
		else if (e.key === "ArrowDown" && first) first.focus();
		else if (e.key === "Enter" && first) location.href = first.href;
		else return;
		e.preventDefault();
	});

	// The build stamp counts the seconds since the binary was built. It is
	// looked up on each tick, since a doc's update replaces it.
	setInterval(() => {
		const ago = document.querySelector("p.built[data-built] > span");
		if (ago) ago.textContent = Math.max(0, Math.floor((Date.now() - Number(ago.parentNode.dataset.built)) / 1000));
	}, 1000);

	// Arrows walk a listing, or the search results: Up and Down move
	// between entries, Right or Space opens one, as Enter does, and Left
	// goes up a directory. Other pages keep their arrows for scrolling. A
	// slash goes to the search box.
	document.addEventListener("keydown", (e) => {
		if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || e.defaultPrevented) return;
		if (e.target.closest && e.target.closest("input, textarea, select, [contenteditable]")) return;
		if (e.key === "/" && box()) {
			e.preventDefault();
			return box().focus();
		}
		if (e.key === "Escape" && searching()) {
			e.preventDefault();
			return clearSearch();
		}
		const links = [...document.querySelectorAll("ul.listing a")].filter((a) => a.offsetParent !== null);
		if (!links.length) return;
		const i = links.indexOf(document.activeElement);
		if (e.key === "ArrowUp" && i === 0 && searching()) {
			e.preventDefault();
			return box().focus();
		}
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
