// Command public is the executable for the open web: it serves only
// repos fetched from a forge, never a tree of this machine.
package main

import "www"

func main() { www.Public() }
