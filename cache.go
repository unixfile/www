package main

import (
	linked "container/list"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// defaultCache is the memory a server may spend on cached answers, eg
// which repo a directory lies in and the commits a page's history lists.
const defaultCache = 32 << 20

// cache keeps values up to a byte budget and drops the least recently
// used first. A budget of 0 keeps nothing.
type cache struct {
	mu    sync.Mutex
	max   int64
	used  int64
	order *linked.List // front is the most recently used
	items map[string]*linked.Element
}

type cacheItem struct {
	key  string
	val  any
	size int64
}

func newCache(max int64) *cache {
	return &cache{max: max, order: linked.New(), items: map[string]*linked.Element{}}
}

// gitCache holds git's answers for every tree on the server; serve sets
// its budget from --cache.
var gitCache = newCache(defaultCache)

func (c *cache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(e)
	return e.Value.(*cacheItem).val, true
}

// put stores val, whose size counts its key too. A value larger than the
// whole budget is not kept.
func (c *cache) put(key string, val any, size int64) {
	size += int64(len(key))
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.remove(e)
	}
	if size > c.max {
		return
	}
	c.items[key] = c.order.PushFront(&cacheItem{key, val, size})
	c.used += size
	for c.used > c.max {
		c.remove(c.order.Back())
	}
}

func (c *cache) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.remove(e)
	}
}

func (c *cache) remove(e *linked.Element) {
	it := c.order.Remove(e).(*cacheItem)
	delete(c.items, it.key)
	c.used -= it.size
}

// parseSize reads a byte count such as 32M, 512K, 1G or 0, in powers of
// 1024.
func parseSize(s string) (int64, error) {
	bad := &errno{syscall.EINVAL, "bad size " + s + ": want a number with K, M or G, eg 32M"}
	num, mult := s, int64(1)
	for suffix, m := range map[string]int64{"K": 1 << 10, "M": 1 << 20, "G": 1 << 30} {
		if strings.HasSuffix(strings.ToUpper(s), suffix) {
			num, mult = s[:len(s)-1], m
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n < 0 || n > (1<<40)/mult {
		return 0, bad
	}
	return n * mult, nil
}
