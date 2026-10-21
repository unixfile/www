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

	// The box under the header searches what is below it. A listing
	// searches its directory, names first, then lines, and the results
	// stand in for the page: its own entries first, then what lies deeper,
	// dimmed. Any other page filters itself in place: lines
	// of code, blocks of a doc, commits of a history, each match marked.
	// The directory's results follow, dimmed, without this file. Escape or
	// an empty box brings the page back, with any update that arrived
	// meanwhile. The query rides in the URL as ?q=, so Back from a result
	// and a reload show the results again.
	let pending = false;
	let timer = null;
	let ctl = null;
	let hidden = [];
	let firstMatch = null;
	const box = () => document.querySelector("input.search");
	const searching = () => {
		const b = box();
		return !!b && b.value.trim() !== "";
	};
	const filters = () => ["code", "doc", "log", "commit"].includes(document.body.className);
	const unfilter = () => {
		for (const el of hidden) el.hidden = false;
		hidden = [];
		firstMatch = null;
		for (const el of document.querySelectorAll(".filtering, p.unfound")) {
			if (el.matches("p.unfound")) el.remove();
			else el.classList.remove("filtering");
		}
		if (window.CSS && CSS.highlights) CSS.highlights.delete("search");
	};
	const showPage = () => {
		const results = document.querySelector("div.found");
		if (results) results.remove();
		unfilter();
		const main = document.querySelector("main");
		if (main) main.hidden = false;
		if (pending) {
			pending = false;
			update();
		}
	};
	// mark adds a range for each match of q in el's text, across the
	// spans that colour it.
	const mark = (el, q, ranges) => {
		const walk = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
		const nodes = [];
		let text = "";
		for (let n; (n = walk.nextNode()); ) {
			nodes.push([n, text.length]);
			text += n.data;
		}
		const low = text.toLowerCase();
		if (low.length !== text.length) return;
		const at = (i) => {
			let k = nodes.length - 1;
			while (nodes[k][1] > i) k--;
			return [nodes[k][0], i - nodes[k][1]];
		};
		for (let i = low.indexOf(q); i >= 0; i = low.indexOf(q, i + q.length)) {
			const r = new Range();
			r.setStart(...at(i));
			r.setEnd(...at(i + q.length));
			ranges.push(r);
		}
	};
	// keep reports whether el holds q, hiding it if not, and marks it if so.
	const keep = (el, q, ranges) => {
		if (!el.textContent.toLowerCase().includes(q)) {
			el.hidden = true;
			hidden.push(el);
			return false;
		}
		if (!firstMatch) firstMatch = el;
		mark(el, q, ranges);
		return true;
	};
	// filter hides what in el does not hold q and reports whether anything
	// is left. Lines of code and of diffs, list items, table rows and a
	// container's children are weighed one by one; any other block stays
	// or goes whole. A file's name in a diff stays while its lines do.
	const filter = (el, q, ranges) => {
		let parts = null;
		if (el.matches("div.sourceCode")) {
			const pre = el.querySelector("pre");
			pre.classList.add("filtering");
			parts = [...pre.querySelectorAll("code > span")];
			for (const a of pre.querySelectorAll("code > span[id] > a:first-child")) a.dataset.n = a.parentNode.id.slice(1);
		} else if (el.matches("pre.diff, ul, ol")) parts = [...el.children];
		else if (el.matches("table")) parts = [...el.querySelectorAll(":scope > tbody > tr")];
		else if (el.matches("main, div")) {
			let any = false;
			for (const c of el.children) if (filter(c, q, ranges)) any = true;
			for (const p of el.querySelectorAll(":scope > p.file")) {
				const next = p.nextElementSibling;
				if (p.hidden && next && next.matches("pre.diff") && !next.hidden) p.hidden = false;
			}
			if (!any) {
				el.hidden = true;
				hidden.push(el);
			}
			return any;
		} else return keep(el, q, ranges);
		let any = false;
		for (const p of parts) if (keep(p, q, ranges)) any = true;
		if (!any) {
			el.hidden = true;
			hidden.push(el);
		}
		return any;
	};
	const filterPage = (q) => {
		unfilter();
		const main = document.querySelector("main");
		const ranges = [];
		if (!filter(main, q.toLowerCase(), ranges)) {
			main.hidden = false;
			const p = document.createElement("p");
			p.className = "unfound";
			p.textContent = "nothing on this page";
			main.after(p);
		}
		if (window.Highlight && CSS.highlights) CSS.highlights.set("search", new Highlight(...ranges));
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
		const inPlace = filters();
		if (inPlace) filterPage(q);
		ctl = new AbortController();
		fetch(b.dataset.search + "?search=" + encodeURIComponent(q), { signal: ctl.signal })
			.then((r) => r.json())
			.then((res) => {
				if (!box() || box().value.trim() !== q) return;
				const own = (h) => inPlace && new URL(h.href, location.href).pathname === location.pathname;
				const list = (wider) => {
					const ul = document.createElement("ul");
					ul.className = "listing" + (wider ? " wider" : "");
					return ul;
				};
				// A listing shows its own entries at full strength and what
				// lies deeper dimmed below; a filtered page dims all of it.
				const near = inPlace ? null : list(false);
				const far = list(true);
				const add = (h, label, text) => {
					if (own(h)) return;
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
					(near && h.here ? near : far).append(li);
				};
				for (const h of res.names) add(h, h.path);
				for (const h of res.lines) add(h, h.path + ":" + h.line, h.text);
				const results = document.createElement("div");
				results.className = "found";
				const line = (cls, text) => {
					const p = document.createElement("p");
					p.className = cls;
					p.textContent = text;
					results.append(p);
				};
				const any = (near ? near.children.length : 0) + far.children.length;
				if (near && near.children.length) results.append(near);
				else if (near && any) line("unfound", "nothing in this directory itself");
				if (far.children.length) results.append(far);
				line("note", (any ? "" : "nothing found, ") + "searched " + res.files + " files" + (res.cut ? ", stopped early" : ""));
				const main = document.querySelector("main");
				const old = document.querySelector("div.found");
				if (old) old.replaceWith(results);
				else if (inPlace) (document.querySelector("p.unfound") || main).after(results);
				else main.before(results);
				if (!inPlace) main.hidden = true;
			})
			.catch(() => {});
	};
	const clearSearch = () => {
		const b = box();
		b.value = "";
		b.blur();
		runSearch();
	};
	// jump leaves the search for a match in the whole page: a line of code
	// by its anchor, anything else scrolled to the middle.
	const jump = (el) => {
		clearSearch();
		if (el.id) location.hash = el.id;
		else el.scrollIntoView({ block: "center" });
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
		const first = document.querySelector("div.found a");
		if (e.key === "Escape") clearSearch();
		else if (e.key === "ArrowDown" && first) first.focus();
		else if (e.key === "Enter" && firstMatch) jump(firstMatch);
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
