(() => {
	let url = document.currentScript.dataset.live;
	let es = null;
	// A page outlives its server. A stream lost for three seconds puts a
	// line over the page that says the server is gone and since when, and
	// the stream tries again each second. A server that answers again
	// sends reload when it is a new one, eg after make run or --restart,
	// and the page takes the new version; a line then says a new server
	// answered and fades. A restart's new server answers within a second
	// or two, before the gone line shows.
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
		} else if (!es && url) {
			es = new EventSource(url);
			es.onmessage = () => (searching() ? (pending = true) : update());
			es.onopen = () => {
				found();
				const gone = document.querySelector("p.conn:not(.fade)");
				if (gone) gone.remove();
			};
			es.onerror = () => {
				if (!lost) lost = setTimeout(() => say("server gone since " + new Date().toTimeString().slice(0, 8) + ", waiting for it", true), 3000);
				// The browser gives up on a stream that met an error page,
				// so a new one takes its place.
				if (es && es.readyState === EventSource.CLOSED) {
					es = null;
					setTimeout(sync, 1000);
				}
			};
		}
	};
	// say shows text in the line over the page, kept until a server
	// answers or fading.
	const say = (text, keep) => {
		const old = document.querySelector("p.conn");
		if (old) old.remove();
		const p = document.createElement("p");
		p.className = "own conn" + (keep ? "" : " fade");
		p.textContent = text;
		if (!keep) p.addEventListener("animationend", () => p.remove());
		document.body.append(p);
	};
	// Each server names itself in the stream's URL. A tab that last saw
	// another says so, whether a reload or a click brought it here.
	const serverKey = "server";
	const seeServer = () => {
		if (!url) return;
		const id = (new URL(url, location.href).searchParams.get("v") || "").split(".")[0];
		let last = null;
		try {
			last = sessionStorage.getItem(serverKey);
			sessionStorage.setItem(serverKey, id);
		} catch {}
		if (last && id && last !== id) say("new server", false);
	};

	// A highlight, not focus, marks the entry the keys act on, since the
	// search box keeps focus. A reload keeps the highlighted entry, and the
	// page Escape goes up to highlights the page it came from, so Enter
	// goes back. Entries match by path and query, without ?q=.
	const selKey = "sel";
	let sel = null;
	let pinnedFrom = null; // the current file's path, while a range pins the address
	const plain = (href) => {
		const u = new URL(href, location.href);
		const rest = u.search.slice(1).split("&").filter((s) => s && s !== "q" && !s.startsWith("q="));
		return u.pathname + (rest.length ? "?" + rest.join("&") : "");
	};
	const select = (el) => {
		if (sel) sel.classList.remove("sel");
		sel = el;
		if (!el) return;
		el.classList.add("sel");
		el.scrollIntoView({ block: "nearest" });
	};
	// hold gives the box the keyboard, except on a touch screen, where
	// focus would raise the on-screen keyboard.
	const hold = () => {
		const b = box();
		if (b && matchMedia("(pointer: fine)").matches) b.focus({ preventScroll: true });
	};
	const reload = () => {
		try {
			if (sel && sel.matches("ul.listing a")) sessionStorage.setItem(selKey, plain(sel.href));
		} catch {}
		// A box's range may have pinned the address to a version; the
		// change is in the current file, so the reload goes there.
		if (pinnedFrom) location.replace(pinnedFrom + location.search + location.hash);
		else location.reload();
	};
	document.addEventListener("DOMContentLoaded", () => {
		let key = null;
		try {
			key = sessionStorage.getItem(selKey);
			sessionStorage.removeItem(selKey);
		} catch {}
		const a = key && [...document.querySelectorAll("main ul.listing a")].find((a) => plain(a.href) === key);
		if (a) select(a);
		hold();
	});

	// A doc takes its new version in place, and each block that differs
	// from the one shown before fills with the target colour and fades.
	// Any other page, a page from a new stylesheet, or a doc that is gone
	// reloads whole.
	const blockKey = (el) => {
		// The top line counts by what it says, not whether its menu
		// was opened.
		if (el.matches("details.latest")) return el.querySelector("summary").outerHTML;
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
					const k = blockKey(el);
					if (shown.get(k)) shown.set(k, shown.get(k) - 1);
					else el.classList.add("changed");
				}
				const x = scrollX, y = scrollY;
				const held = document.activeElement === box();
				document.title = next.title;
				document.body.replaceWith(document.adoptNode(next.body));
				scrollTo(x, y);
				sel = null;
				if (held) hold();
				backlinks();
				measureSearch();
				url = live.dataset.live;
				seeServer();
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
	let matches = [];
	const box = () => document.querySelector("input.search");
	const searching = () => {
		const b = box();
		return !!b && b.value.trim() !== "";
	};
	const filters = () => ["code", "doc", "log", "commit"].includes(document.body.className);
	const unfilter = () => {
		for (const el of hidden) el.hidden = false;
		hidden = [];
		matches = [];
		select(null);
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
		matches.push(el);
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
				const recents = recentMatching(q);
				const shown = new Set(recents);
				const add = (h, label, text) => {
					if (own(h) || (!h.line && shown.has(new URL(h.href, location.href).pathname))) return;
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
				// Pages the reader opened that match lead, from any tree.
				const recent = list(false);
				recent.classList.add("recent");
				for (const p of recents) {
					const li = document.createElement("li");
					const a = document.createElement("a");
					a.href = p;
					a.textContent = recentLabel(p);
					li.append(a);
					recent.append(li);
				}
				if (recent.children.length) {
					const p = document.createElement("p");
					p.className = "note";
					p.textContent = "recent";
					results.prepend(p, recent);
				}
				// A forge's URL leads, read on the web address; Enter opens it.
				if (res.web) {
					const web = list(false);
					web.classList.add("web");
					const li = document.createElement("li");
					const a = document.createElement("a");
					a.href = res.web.href;
					a.textContent = res.web.path;
					li.append(a);
					web.append(li);
					const p = document.createElement("p");
					p.className = "note";
					p.textContent = "online";
					results.prepend(p, web);
				}
				const any = (res.web ? 1 : 0) + recent.children.length + (near ? near.children.length : 0) + far.children.length;
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
				// Typed further down a listing, the results begin above the
				// window; they come into view under the row.
				if (!inPlace && results.getBoundingClientRect().top < 0) results.scrollIntoView({ block: "start" });
			})
			.catch(() => {});
	};
	const clearSearch = () => {
		box().value = "";
		runSearch();
		settle();
	};
	// jump leaves the search for a match in the whole page: a line of code
	// by its anchor, anything else scrolled to the middle.
	const jump = (el) => {
		clearSearch();
		if (el.id) location.hash = el.id;
		else el.scrollIntoView({ block: "center" });
		hold();
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
		settle();
	});

	// The controls fade while none is needed: the box, copy and share, raw,
	// the stop button and a code page's views. They show while the query
	// has a character, while the mouse moves inside the column, while
	// keyboard focus is on one of them or a crumb, and after a tap on
	// their rows on a touch screen. They fade 2 s after the mouse stops and
	// at once when it leaves the column. Focus in the empty box does not
	// count, since the box holds focus on every page.
	const chrome = "header, div.search, div.view";
	// While calmTimer runs, the mouse or a tap keeps the controls shown.
	let calmTimer = null;
	const needed = () => {
		const f = document.activeElement;
		return searching() || (!!f && !!f.closest && !!f.closest(chrome) && !f.matches("input.search"));
	};
	const calm = () => {
		clearTimeout(calmTimer);
		calmTimer = null;
		if (!needed()) document.documentElement.classList.add("calm");
	};
	const wake = () => {
		clearTimeout(calmTimer);
		document.documentElement.classList.remove("calm");
		calmTimer = setTimeout(calm, 2000);
	};
	// settle runs when the need changes: the query, or focus.
	const settle = () => {
		if (needed()) document.documentElement.classList.remove("calm");
		else if (!calmTimer) calm();
	};
	document.addEventListener("pointermove", (e) => {
		if (e.pointerType === "touch") return;
		const r = document.body.getBoundingClientRect();
		if (e.clientX >= r.left && e.clientX <= r.right) wake();
		else calm();
	});
	// A finger leaves the page at the end of every tap, so only the mouse
	// leaving counts.
	document.documentElement.addEventListener("pointerleave", (e) => {
		if (e.pointerType !== "touch") calm();
	});
	document.addEventListener("pointerdown", (e) => {
		if (e.pointerType === "touch" && e.target.closest && e.target.closest(chrome)) wake();
	});
	document.addEventListener("focusin", settle);
	document.addEventListener("focusout", () => setTimeout(settle));
	document.addEventListener("DOMContentLoaded", calm);

	// Below a doc, the docs that link to it. The server walks the tree for
	// them, which can take longer than the page itself, so they arrive
	// after it, and again after each update, which replaces the page.
	const backlinks = () => {
		if (document.body.className !== "doc") return;
		fetch(location.pathname + "?backlinks", { cache: "no-store" })
			.then((r) => (r.ok ? r.json() : Promise.reject()))
			.then((res) => {
				const main = document.querySelector("main");
				if (!main || document.body.className !== "doc") return;
				const old = main.querySelector(":scope > div.backlinks");
				if (old) old.remove();
				if (!res.links.length) return;
				const div = document.createElement("div");
				div.className = "backlinks own";
				const h = document.createElement("h2");
				h.className = "own";
				h.textContent = "linked from";
				const ul = document.createElement("ul");
				for (const l of res.links) {
					const li = document.createElement("li");
					const a = document.createElement("a");
					a.href = l.href;
					a.textContent = l.path;
					li.append(a);
					ul.append(li);
				}
				div.append(h, ul);
				main.append(div);
				if (searching() && filters()) filterPage(box().value.trim());
			})
			.catch(() => {});
	};

	// The pages a reader opened live in the browser's storage for this
	// origin, never on the server, so each viewer of a share sees only
	// what that viewer opened. Each entry counts its visits and keeps the
	// last; the score weighs both, as zoxide does. A reload is no visit.
	const recentKey = "recent";
	const maxRecent = 300;
	const readRecent = () => {
		try {
			const list = JSON.parse(localStorage.getItem(recentKey));
			return Array.isArray(list) ? list : [];
		} catch {
			return [];
		}
	};
	const score = (e, now) => {
		const age = now - e.t;
		return e.n * (age < 3600e3 ? 4 : age < 86400e3 ? 2 : age < 7 * 86400e3 ? 0.5 : 0.25);
	};
	const recentPaths = () => {
		const now = Date.now();
		return readRecent()
			.sort((a, b) => score(b, now) - score(a, now))
			.map((e) => e.p);
	};
	const recentLabel = (p) => {
		try {
			return decodeURIComponent(p).replace(/^\//, "");
		} catch {
			return p;
		}
	};
	// recentMatching lists the five best pages whose path holds every word
	// of q, without this page.
	const recentMatching = (q) => {
		const words = q.toLowerCase().split(/\s+/).filter((w) => w);
		return recentPaths()
			.filter((p) => p !== location.pathname && words.every((w) => recentLabel(p).toLowerCase().includes(w)))
			.slice(0, 5);
	};
	const visit = () => {
		if (!["doc", "code", "dir"].includes(document.body.className)) return;
		const nav = performance.getEntriesByType("navigation")[0];
		if (nav && nav.type === "reload") return;
		const now = Date.now();
		const list = readRecent();
		const e = list.find((e) => e.p === location.pathname);
		if (e) {
			e.n++;
			e.t = now;
		} else list.push({ p: location.pathname, n: 1, t: now });
		list.sort((a, b) => score(b, now) - score(a, now));
		try {
			localStorage.setItem(recentKey, JSON.stringify(list.slice(0, maxRecent)));
		} catch {}
	};
	document.addEventListener("DOMContentLoaded", () => {
		seeServer();
		visit();
		backlinks();
	});

	// The bug button, at each page's foot, files a numbered bug in the
	// program itself, not in the tree it shows. It sends the page's URL, the
	// selected text as the note, the window's size and scroll, the browser,
	// and a snapshot of the page as it stands, scripts taken out, so the
	// snapshot shows what the reader saw without running again; the server
	// adds its own state. The button then says the number, and a field
	// beside it takes a comment: Enter adds it, Escape or leaving the field
	// drops it.
	const bugSaid = (b, word) => {
		b.dataset.label ??= b.textContent;
		b.textContent = word;
		setTimeout(() => (b.textContent = b.dataset.label), 3000);
	};
	const bugComment = (b, n) => {
		const f = document.createElement("input");
		f.placeholder = "comment on bug " + n;
		f.setAttribute("aria-label", "comment on bug " + n);
		b.before(f);
		f.focus();
		let done = false;
		const end = () => {
			if (done) return;
			done = true;
			f.remove();
		};
		f.addEventListener("keydown", (e) => {
			if (e.key === "Escape") return end();
			if (e.key !== "Enter" || !f.value.trim()) return;
			const body = new URLSearchParams({ bug: n, comment: f.value.trim() });
			end();
			fetch(b.dataset.post, { method: "POST", body }).then(
				(r) => bugSaid(b, r.ok ? "comment on bug " + n + " added" : "comment failed"),
				() => bugSaid(b, "comment failed"),
			);
		});
		f.addEventListener("blur", end);
	};
	document.addEventListener("click", (e) => {
		const b = e.target.closest("button.bug");
		if (!b) return;
		const page = document.documentElement.cloneNode(true);
		for (const s of page.querySelectorAll("script")) s.remove();
		const inputs = page.querySelectorAll("input");
		document.querySelectorAll("input").forEach((el, i) => inputs[i] && inputs[i].setAttribute("value", el.value));
		const base = document.createElement("base");
		base.href = location.href;
		page.querySelector("head").prepend(base);
		const body = new URLSearchParams({
			url: location.href,
			note: String(getSelection()).trim(),
			view: innerWidth + "x" + innerHeight + "+" + Math.round(scrollY),
			ua: navigator.userAgent,
			page: "<!doctype html>\n" + page.outerHTML,
		});
		fetch(b.dataset.post, { method: "POST", body })
			.then((r) => (r.ok ? r.text() : Promise.reject()))
			.then(
				(n) => {
					bugSaid(b, "bug " + n + " filed");
					bugComment(b, n);
				},
				() => bugSaid(b, "failed"),
			);
	});

	// The build stamp counts the seconds since the binary was built. It is
	// looked up on each tick, since a doc's update replaces it.
	setInterval(() => {
		const ago = document.querySelector("p.built[data-built] > span");
		if (ago) ago.textContent = Math.max(0, Math.floor((Date.now() - Number(ago.parentNode.dataset.built)) / 1000));
	}, 1000);

	// The box holds the keyboard, so typing searches from anywhere: a key
	// that types, or Backspace during a search, goes to the box wherever
	// focus is. Up and Down move the highlight through what can be opened,
	// a listing's entries or a search's matches and results, and Enter
	// opens it, or the first one during a search. Left and Right stay with
	// the box's caret. Escape clears the query, and with none goes up to
	// the last linked crumb. With nothing to walk, Up, Down and Space
	// scroll the page as they would outside the box; during a search Space
	// types. Shift+Tab in the box enters the header at the crumb Escape
	// goes to, so Delete lock is never one key from the box; Escape on any
	// other focused control comes back to the box.
	const openMenu = () => document.querySelector("details.latest[open]");
	const walkable = () =>
		openMenu() ? [...openMenu().querySelectorAll("ol.versions a")] : (searching() ? [...document.querySelectorAll("div.found ul.web a"), ...matches, ...document.querySelectorAll("div.found ul:not(.web) a")] : [...document.querySelectorAll("main ul.listing a")])
			.filter((el) => el.offsetParent !== null);
	const up = () => {
		const crumbs = document.querySelectorAll("header nav a");
		if (!crumbs.length) return;
		try {
			sessionStorage.setItem(selKey, plain(location.href));
		} catch {}
		location.href = crumbs[crumbs.length - 1].href;
	};
	document.addEventListener("keydown", (e) => {
		if (e.altKey || e.ctrlKey || e.metaKey || e.isComposing || e.defaultPrevented) return;
		const b = box();
		const inBox = !!b && e.target === b;
		if (!inBox && e.target.closest && e.target.closest("input, textarea, select, [contenteditable]")) return;
		if (inBox && e.key === "Tab" && e.shiftKey) {
			const crumbs = document.querySelectorAll("header nav a");
			if (!crumbs.length) return;
			e.preventDefault();
			return crumbs[crumbs.length - 1].focus();
		}
		const q = searching();
		const presses = e.target.closest && e.target.closest("button, summary");
		if (b && !inBox && ((e.key.length === 1 && (e.key !== " " || (q && !presses))) || (e.key === "Backspace" && q))) {
			b.focus({ preventScroll: true });
			b.selectionStart = b.selectionEnd = b.value.length;
			return;
		}
		const walk = walkable();
		const i = walk.indexOf(sel);
		switch (e.key) {
			case "ArrowDown":
			case "ArrowUp":
				if (walk.length) {
					if (e.key === "ArrowDown") select(walk[Math.min(i + 1, walk.length - 1)]);
					else select(walk[i < 0 ? walk.length - 1 : Math.max(i - 1, 0)]);
					if (b) b.focus({ preventScroll: true });
				} else if (inBox) scrollBy(0, e.key === "ArrowDown" ? 40 : -40);
				else return;
				break;
			case "Enter": {
				if (!inBox && e.target.closest && e.target.closest("a, button, summary")) return;
				const to = i >= 0 ? sel : q ? walk[0] : null;
				if (!to) return;
				if (to.matches("a")) location.href = to.href;
				else jump(to);
				break;
			}
			case "Escape":
				if (openMenu()) openMenu().open = false;
				else if (b && !inBox && document.activeElement !== document.body) b.focus({ preventScroll: true });
				else if (q) clearSearch();
				else up();
				break;
			case " ":
				if (!inBox || q) return;
				scrollBy(0, (e.shiftKey ? -0.85 : 0.85) * innerHeight);
				break;
			default:
				return;
		}
		e.preventDefault();
	});

	// A line's number marks the line and puts it in the address, eg #L42;
	// clicking the marked line's number again clears both. An empty
	// fragment unmarks the line, since replaceState alone leaves :target
	// as it was; the scroll position is kept, and the bare # dropped.
	document.addEventListener("click", (e) => {
		const a = e.target.closest && e.target.closest("pre.numberSource code > span > a:first-child");
		if (!a || a.getAttribute("href") !== location.hash) return;
		e.preventDefault();
		const x = scrollX, y = scrollY;
		location.hash = "";
		scrollTo(x, y);
		history.replaceState(history.state, "", location.pathname + location.search);
		hold();
	});

	// A code page's boxes are links to their lines. A click on a box,
	// where no inner box lies, marks the box and the boxes in it and puts
	// its lines in the address, eg #L482-L520; a click on the marked box
	// clears both. The box under the pointer is tinted, so it shows what a
	// click marks. Boxes take no pointer events, so the hit test is by
	// geometry, measured once against the pre, which scrolls sideways.
	// A file committed and unchanged since gets its address pinned to that
	// commit's version, eg @261024.4/main.go, so the range names these
	// lines after the file changes; the timeline marks that version with
	// data-also only when the file is clean. A range in the address marks
	// lines on any numbered page, and one box when it matches one.
	const rangeOf = (hash) => {
		const m = /^#L(\d+)-L(\d+)$/.exec(hash);
		return m && +m[1] <= +m[2] ? [+m[1], +m[2]] : null;
	};
	const cssNum = (el, v) => +el.style.getPropertyValue(v);
	let boxes = null;
	const measure = (pre) => {
		if (boxes) return boxes;
		const p = pre.getBoundingClientRect();
		boxes = [...pre.querySelectorAll("i.scope")].map((el) => {
			const r = el.getBoundingClientRect();
			const first = +el.parentElement.id.slice(1);
			const leaf = el.classList.contains("leaf");
			return {
				el, leaf, first, last: leaf ? first : first + cssNum(el, "--r") - 1,
				c: cssNum(el, "--c"), w: cssNum(el, "--w"), d: cssNum(el, "--d"),
				left: r.left - p.left, right: r.right - p.left, top: r.top - p.top, bottom: r.bottom - p.top,
			};
		});
		return boxes;
	};
	addEventListener("resize", () => (boxes = null));
	const boxAt = (pre, x, y) => {
		if (pre.classList.contains("filtering")) return (boxes = null);
		const p = pre.getBoundingClientRect();
		x -= p.left;
		y -= p.top;
		let best = null;
		for (const b of measure(pre))
			if (!b.leaf && (!best || b.d > best.d) && x >= b.left && x < b.right && y >= b.top && y < b.bottom) best = b;
		return best;
	};
	let hovered = null;
	const hover = (b) => {
		if (hovered === b) return;
		if (hovered) hovered.el.classList.remove("hover");
		hovered = b;
		if (b) b.el.classList.add("hover");
	};
	let moved = null;
	document.addEventListener("pointermove", (e) => {
		const pre = e.target.closest && e.target.closest("pre.scopes");
		if (!pre) return hover(null);
		if (moved) return (moved = e);
		moved = e;
		requestAnimationFrame(() => {
			hover(boxAt(pre, moved.clientX, moved.clientY));
			moved = null;
		});
	});
	const showRange = () => {
		for (const el of document.querySelectorAll(".marked")) el.classList.remove("marked");
		const r = rangeOf(location.hash);
		if (!r) return;
		const pre = document.querySelector("pre.scopes");
		const box = pre && measure(pre).find((b) => !b.leaf && b.first === r[0] && b.last === r[1]);
		if (box) {
			for (const b of boxes)
				if (b.first >= box.first && b.last <= box.last && b.c >= box.c && b.c + b.w <= box.c + box.w) b.el.classList.add("marked");
			return;
		}
		for (let n = r[0]; n <= r[1]; n++) {
			const line = document.getElementById("L" + n);
			if (line && line.matches("pre.numberSource code > span")) line.classList.add("marked");
		}
	};
	const pin = (hash) => {
		const d = document.querySelector("details.latest");
		if (!d || d.dataset.here || pinnedFrom) return;
		versions(d)
			.then(() => {
				const li = d.querySelector("ol.versions li[data-also]");
				if (!li || location.hash !== hash) return;
				// The pin names the lines; it does not move the reader.
				// Relative links, eg the views' ?raw, would resolve
				// against the version, so they are made absolute first.
				for (const a of document.querySelectorAll("a[href]")) {
					const h = a.getAttribute("href");
					if (!/^([a-z][a-z0-9+.-]*:|\/|#)/i.test(h)) a.setAttribute("href", a.href);
				}
				pinnedFrom = location.pathname;
				history.replaceState(history.state, "", li.dataset.also + location.search + hash);
			})
			.catch(() => {});
	};
	const markRange = (hash) => {
		const x = scrollX, y = scrollY;
		// A line marked by its number is :target, which only a new
		// fragment clears.
		if (document.querySelector(":target")) location.hash = "";
		let path = location.pathname;
		if (!hash && pinnedFrom) {
			path = pinnedFrom;
			pinnedFrom = null;
		}
		history.replaceState(history.state, "", path + location.search + hash);
		scrollTo(x, y);
		showRange();
		if (hash) pin(hash);
	};
	document.addEventListener("click", (e) => {
		const pre = e.target.closest && e.target.closest("pre.scopes");
		if (!pre || e.target.closest("a") || !getSelection().isCollapsed) return;
		const b = boxAt(pre, e.clientX, e.clientY);
		if (!b) return;
		const hash = "#L" + b.first + "-L" + b.last;
		markRange(location.hash === hash ? "" : hash);
		hold();
	});
	addEventListener("hashchange", showRange);
	document.addEventListener("DOMContentLoaded", () => {
		const r = rangeOf(location.hash);
		if (!r) return;
		showRange();
		const line = document.getElementById("L" + r[0]);
		if (line) line.scrollIntoView({ block: "start" });
	});

	// The copy button copies the page's text: what lies under the search
	// box as the browser lays it out, without the program's own lines, the
	// view links, line numbers or what a search hides. Lines marked by a
	// range in the address copy alone, and a doc copies its Markdown, from
	// ?raw. It copies through the clipboard API where the origin is
	// secure, else through a selection, which works on any origin. Its
	// glyph says how it went: a tick or a cross, then the copy glyph again.
	const pageText = () => {
		const main = document.querySelector("main");
		if (!main) return "";
		document.body.classList.add("copying");
		const text = main.innerText;
		document.body.classList.remove("copying");
		return text.replace(/\s+$/, "") + "\n";
	};
	const rangeText = () => {
		const r = rangeOf(location.hash);
		if (!r) return null;
		const lines = [];
		for (let n = r[0]; n <= r[1]; n++) {
			const line = document.getElementById("L" + n);
			if (!line || !line.matches("pre.numberSource code > span")) return null;
			lines.push(line.textContent);
		}
		return lines.join("\n") + "\n";
	};
	// A doc's Markdown is fetched when the pointer or focus reaches the
	// button, so the click writes at once: a browser lets a click write
	// the clipboard only for a moment after it. A doc that changes in
	// place is fetched again on the next approach.
	let source = null;
	const docSource = () => {
		if (!source)
			source = fetch((pinnedFrom || location.pathname) + "?raw", { cache: "no-store" })
				.then((r) => (r.ok ? r.text() : Promise.reject()))
				.catch(() => {
					source = null;
					return Promise.reject();
				});
		return source;
	};
	const approach = (e) => {
		if (document.body.className === "doc" && e.target.closest && e.target.closest("button.copy")) {
			source = null;
			docSource().catch(() => {});
		}
	};
	document.addEventListener("pointerover", approach);
	document.addEventListener("focusin", approach);
	// clip writes text to the clipboard for the button btn, whose glyph
	// then says how it went and comes back after a moment.
	const clip = (btn, text) => {
		const glyph = btn.textContent, title = btn.title;
		const done = (ok) => {
			btn.textContent = ok ? "✓" : "✗";
			btn.title = ok ? "copied" : "copy failed";
			setTimeout(() => {
				btn.textContent = glyph;
				btn.title = title;
			}, 1500);
			hold();
		};
		const bySelection = (text) => {
			const t = document.createElement("textarea");
			t.value = text;
			t.style.position = "fixed";
			t.style.opacity = "0";
			document.body.append(t);
			t.select();
			let ok = false;
			try {
				ok = document.execCommand("copy");
			} catch {}
			t.remove();
			done(ok);
		};
		const write = (text) => {
			if (navigator.clipboard && window.isSecureContext)
				navigator.clipboard.writeText(text).then(() => done(true), () => bySelection(text));
			else bySelection(text);
		};
		if (typeof text === "string") write(text);
		else text.then(write, () => done(false));
	};
	document.addEventListener("click", (e) => {
		const btn = e.target.closest && e.target.closest("button.copy");
		if (!btn) return;
		const lines = rangeText();
		if (lines !== null) return clip(btn, lines);
		if (document.body.className === "doc") return clip(btn, docSource());
		clip(btn, pageText());
	});

	// The share button copies a link to what is selected: lines selected
	// or marked on a numbered page, eg #L3-L26, pinned to the commit's
	// version when the file is committed and unchanged since, as a box's
	// link is; other selected text as a text fragment, #:~:text=, which
	// browsers scroll to and highlight; else the page itself, which on a
	// commit page may be a declaration's page. The link has the address
	// the page came from, so on a share it reaches other machines and on
	// loopback only this one. The version menu that pinning reads loads as
	// the pointer reaches the button, since a click may write the
	// clipboard only at once.
	const numbered = (node) => {
		const el = node && (node.nodeType === 1 ? node : node.parentElement);
		const line = el && el.closest("pre.numberSource code > span");
		return line && /^L\d+$/.test(line.id) ? +line.id.slice(1) : null;
	};
	const selectedLines = () => {
		const sel = getSelection();
		if (!sel || sel.isCollapsed) return null;
		const a = numbered(sel.anchorNode), b = numbered(sel.focusNode);
		if (a === null || b === null) return null;
		return a === b ? "#L" + a : "#L" + Math.min(a, b) + "-L" + Math.max(a, b);
	};
	const fragmentText = () => {
		const sel = getSelection();
		const main = document.querySelector("main");
		if (!sel || sel.isCollapsed || !main || !main.contains(sel.anchorNode)) return null;
		const words = sel.toString().trim().split(/\s+/).filter(Boolean);
		if (!words.length) return null;
		// Commas and dashes are the syntax's own, so they are escaped too.
		const enc = (ws) => encodeURIComponent(ws.join(" ")).replace(/-/g, "%2D");
		if (words.length <= 8) return "#:~:text=" + enc(words);
		return "#:~:text=" + enc(words.slice(0, 4)) + "," + enc(words.slice(-4));
	};
	const pinnedPath = () => {
		if (pinnedFrom) return location.pathname;
		const d = document.querySelector("details.latest");
		const li = d && !d.dataset.here && d.querySelector("ol.versions li[data-also]");
		return li ? li.dataset.also : location.pathname;
	};
	const withoutQuery = () => {
		const rest = location.search.slice(1).split("&").filter((s) => s && s !== "q" && !s.startsWith("q="));
		return rest.length ? "?" + rest.join("&") : "";
	};
	// On the web address the link is the forge's own, at the commit the
	// page shows, which opens in any browser; lines take the forge's form.
	const forgeLines = (hash, style) => {
		const [, a, b] = /^#L(\d+)(?:-L(\d+))?$/.exec(hash);
		if (style === "gitlab") return "#L" + a + (b ? "-" + b : "");
		if (style === "bitbucket") return "#lines-" + a + (b ? ":" + b : "");
		return hash;
	};
	const shareURL = (btn) => {
		const lines = selectedLines() || (/^#L\d+(-L\d+)?$/.test(location.hash) ? location.hash : null);
		const forge = btn.dataset.forge;
		if (forge) {
			if (lines) return forge + forgeLines(lines, btn.dataset.lines);
			return forge + (fragmentText() || "");
		}
		if (lines) return location.origin + pinnedPath() + withoutQuery() + lines;
		const text = fragmentText();
		if (text) return location.origin + location.pathname + withoutQuery() + text;
		return location.href;
	};
	const nearShare = (e) => {
		const d = document.querySelector("details.latest");
		if (d && e.target.closest && e.target.closest("button.share")) versions(d).catch(() => {});
	};
	document.addEventListener("pointerover", nearShare);
	document.addEventListener("focusin", nearShare);
	// A click on the button would collapse the selection before it is
	// read, so the button keeps the pointer from taking focus.
	document.addEventListener("mousedown", (e) => {
		if (e.target.closest && e.target.closest("button.share")) e.preventDefault();
	});
	document.addEventListener("click", (e) => {
		const btn = e.target.closest && e.target.closest("button.share");
		if (btn) clip(btn, shareURL(btn));
	});

	// The top line opens the timeline, the menu of the page's versions.
	// It loads from ?timeline when the pointer first comes over the line,
	// so it is there when it opens, or on opening. The version shown is marked and
	// highlighted, so Up and Down walk from it and Enter opens one; Escape
	// or a click outside closes it.
	// versions loads the menu once and returns the load, which a pin
	// waits for; a failed load is tried again next time.
	const loads = new WeakMap();
	const versions = (d) => {
		if (!loads.has(d))
			loads.set(
				d,
				fetch(d.dataset.timeline)
					.then((r) => (r.ok ? r.text() : Promise.reject()))
					.then((html) => {
						const got = new DOMParser().parseFromString(html, "text/html").querySelector("ol.versions");
						const slot = d.querySelector("div.versions");
						if (!got || !slot) return Promise.reject();
						slot.replaceWith(document.adoptNode(got));
						markHere(d);
					})
					.catch(() => {
						loads.delete(d);
						return Promise.reject();
					}),
			);
		return loads.get(d);
	};
	const markHere = (d) => {
		const at = d.dataset.here || location.pathname;
		const here = [...d.querySelectorAll("ol.versions a")].find(
			(a) => (a.pathname === at && !a.search) || a.parentElement.dataset.also === at,
		);
		if (!here) return;
		here.parentElement.classList.add("here");
		if (d.open) {
			select(here);
			here.parentElement.scrollIntoView({ block: "nearest" });
		}
	};
	document.addEventListener("pointerover", (e) => {
		const d = e.target.closest && e.target.closest("details.latest");
		if (d) versions(d).catch(() => {});
	});
	document.addEventListener("click", (e) => {
		const d = openMenu();
		if (d && !d.contains(e.target)) d.open = false;
	});

	// A history's fold loads its commit's diff from the commit page the
	// first time it opens. toggle does not bubble, so it is caught on the
	// way down.
	document.addEventListener("toggle", (e) => {
		const d = e.target;
		if (d.matches && d.matches("details.latest")) {
			if (!d.open) return select(null);
			versions(d).catch(() => {});
			return markHere(d);
		}
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

	// The search row sticks to the window's top. Its height, in
	// --search-h, keeps a code page's view links below it and stops a
	// jump to a line or heading below it rather than under it. A doc's
	// live update brings a new body, so this runs again after it.
	const measureSearch = () => {
		const row = document.querySelector("div.search");
		document.documentElement.style.setProperty("--search-h", (row ? row.offsetHeight : 0) + "px");
	};
	addEventListener("resize", measureSearch);
	document.addEventListener("DOMContentLoaded", measureSearch);

	// A link another site sent to a repo not yet read lands on a page
	// that asks first; its read link keeps the link's fragment, eg the
	// lines it marks.
	document.addEventListener("DOMContentLoaded", () => {
		if (document.body.className !== "confirm" || !location.hash) return;
		const a = document.querySelector("main a");
		if (a) a.href = a.href.split("#")[0] + location.hash;
	});

	// A commit page shows its plain diff at once and asks for the semantic
	// one after: the declarations the commit changed above the diff, and
	// the changed tokens marked within it. A search on the page runs again
	// over the new diff.
	document.addEventListener("DOMContentLoaded", () => {
		const unread = document.querySelector("div.semantic[data-src]");
		if (!unread) return;
		fetch(unread.dataset.src)
			.then((r) => r.text())
			.then((html) => {
				const got = new DOMParser().parseFromString(html, "text/html");
				const sem = got.querySelector("div.semantic");
				const diffs = got.querySelector("div.diffs");
				const old = document.querySelector("main div.diffs");
				if (!sem || !diffs || !old) return;
				unread.replaceWith(document.adoptNode(sem));
				old.replaceWith(document.adoptNode(diffs));
				if (searching() && filters()) filterPage(box().value.trim());
			})
			.catch(() => unread.remove());
	});
})();
