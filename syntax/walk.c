// walk.c parses the source and writes its named nodes in one call from Go.
// The Go binding crosses cgo for each node it is asked about, about ten
// times a node, anonymous nodes included, which took longer than the
// parse itself; here the walk never leaves C.

#include <errno.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

// The declarations below are copied from tree-sitter's api.h, as
// go-tree-sitter v0.25.0 ships it; cgo has no path to that header in the
// module cache. The layouts of TSPoint, TSNode and TSTreeCursor must
// match it, since they pass by value.
typedef uint16_t TSSymbol;
typedef uint16_t TSFieldId;
typedef struct TSLanguage TSLanguage;
typedef struct TSParser TSParser;
typedef struct TSTree TSTree;

typedef struct TSPoint {
	uint32_t row;
	uint32_t column;
} TSPoint;

typedef struct TSNode {
	uint32_t context[4];
	const void *id;
	const TSTree *tree;
} TSNode;

typedef struct TSTreeCursor {
	const void *tree;
	const void *id;
	uint32_t context[3];
} TSTreeCursor;

TSParser *ts_parser_new(void);
void ts_parser_delete(TSParser *self);
bool ts_parser_set_language(TSParser *self, const TSLanguage *language);
TSTree *ts_parser_parse_string(TSParser *self, const TSTree *old_tree, const char *string, uint32_t length);
void ts_tree_delete(TSTree *self);
TSNode ts_tree_root_node(const TSTree *self);
TSSymbol ts_node_symbol(TSNode self);
TSPoint ts_node_start_point(TSNode self);
TSPoint ts_node_end_point(TSNode self);
bool ts_node_is_named(TSNode self);
bool ts_node_is_missing(TSNode self);
TSTreeCursor ts_tree_cursor_new(TSNode node);
void ts_tree_cursor_delete(TSTreeCursor *self);
TSNode ts_tree_cursor_current_node(const TSTreeCursor *self);
TSFieldId ts_tree_cursor_current_field_id(const TSTreeCursor *self);
bool ts_tree_cursor_goto_parent(TSTreeCursor *self);
bool ts_tree_cursor_goto_next_sibling(TSTreeCursor *self);
bool ts_tree_cursor_goto_first_child(TSTreeCursor *self);
const char *ts_language_symbol_name(const TSLanguage *self, TSSymbol symbol);
const char *ts_language_field_name_for_id(const TSLanguage *self, TSFieldId id);

// out buffers the rows for stdout. err holds the first failed write's
// errno, after which nothing more is written.
struct out {
	char buf[1 << 16];
	size_t n;
	int err;
};

static void flush(struct out *o) {
	size_t done = 0;
	while (done < o->n && o->err == 0) {
		ssize_t w = write(1, o->buf + done, o->n - done);
		if (w < 0) {
			if (errno != EINTR) {
				o->err = errno;
			}
			continue;
		}
		done += (size_t)w;
	}
	o->n = 0;
}

static void put(struct out *o, const char *s, size_t len) {
	if (o->n + len > sizeof o->buf) {
		flush(o);
	}
	// A kind longer than the buffer is not in any grammar, but writes
	// whole all the same.
	if (len > sizeof o->buf) {
		memcpy(o->buf, s, sizeof o->buf);
		o->n = sizeof o->buf;
		flush(o);
		put(o, s + sizeof o->buf, len - sizeof o->buf);
		return;
	}
	memcpy(o->buf + o->n, s, len);
	o->n += len;
}

static void putnum(struct out *o, uint64_t v) {
	char d[20];
	int i = sizeof d;
	do {
		d[--i] = '0' + v % 10;
		v /= 10;
	} while (v > 0);
	put(o, d + i, sizeof d - i);
}

static void puttab(struct out *o) {
	put(o, "\t", 1);
}

// syntax_tree parses src of len bytes by lang and writes the named nodes
// in preorder to stdout, one per line, as main.go's comment describes.
// Anonymous nodes, eg punctuation and keywords, are left out, but their
// named descendants keep their place under the nearest named ancestor.
// It returns 0, EINVAL when the grammar does not load, EIO when the
// parser gives no tree, ENOMEM, or the errno of a failed write.
int syntax_tree(const TSLanguage *lang, const char *src, uint32_t len) {
	TSParser *p = ts_parser_new();
	if (!ts_parser_set_language(p, lang)) {
		ts_parser_delete(p);
		return EINVAL;
	}
	TSTree *tree = ts_parser_parse_string(p, NULL, src, len);
	ts_parser_delete(p);
	if (tree == NULL) {
		return EIO;
	}
	struct out *o = calloc(1, sizeof *o);
	// named holds the cursor depths of the shown nodes above the cursor;
	// a row's depth is how many there are.
	size_t cap = 64, top = 0;
	uint32_t *named = malloc(cap * sizeof *named);
	if (o == NULL || named == NULL) {
		free(o);
		free(named);
		ts_tree_delete(tree);
		return ENOMEM;
	}
	int ret = 0;
	TSTreeCursor cur = ts_tree_cursor_new(ts_tree_root_node(tree));
	uint32_t depth = 0;
	for (;;) {
		TSNode n = ts_tree_cursor_current_node(&cur);
		bool missing = ts_node_is_missing(n);
		bool shown = missing || ts_node_is_named(n);
		if (shown) {
			const char *kind = ts_language_symbol_name(lang, ts_node_symbol(n));
			const char *field = ts_language_field_name_for_id(lang, ts_tree_cursor_current_field_id(&cur));
			TSPoint s = ts_node_start_point(n), e = ts_node_end_point(n);
			putnum(o, top);
			puttab(o);
			if (missing) {
				put(o, "MISSING ", 8);
			}
			if (kind != NULL) {
				put(o, kind, strlen(kind));
			}
			puttab(o);
			if (field != NULL) {
				put(o, field, strlen(field));
			}
			puttab(o);
			putnum(o, s.row);
			puttab(o);
			putnum(o, s.column);
			puttab(o);
			putnum(o, e.row);
			puttab(o);
			putnum(o, e.column);
			put(o, "\n", 1);
			if (o->err != 0) {
				break;
			}
		}
		if (ts_tree_cursor_goto_first_child(&cur)) {
			if (shown) {
				if (top == cap) {
					uint32_t *more = realloc(named, 2 * cap * sizeof *named);
					if (more == NULL) {
						ret = ENOMEM;
						break;
					}
					named = more;
					cap *= 2;
				}
				named[top++] = depth;
			}
			depth++;
			continue;
		}
		bool done = false;
		while (!ts_tree_cursor_goto_next_sibling(&cur)) {
			if (!ts_tree_cursor_goto_parent(&cur)) {
				done = true;
				break;
			}
			depth--;
			if (top > 0 && named[top - 1] == depth) {
				top--;
			}
		}
		if (done) {
			break;
		}
	}
	flush(o);
	if (ret == 0) {
		ret = o->err;
	}
	ts_tree_cursor_delete(&cur);
	ts_tree_delete(tree);
	free(named);
	free(o);
	return ret;
}
