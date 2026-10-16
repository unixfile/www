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
})();
