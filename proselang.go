package www

import (
	"math/bits"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
)

// A doc's prose is hyphenated by the rules of its language, which a
// browser picks from the lang attribute. The page says English; a block
// written in another language says so itself.
//
// The language is told from the commonest words: they make up a third or
// more of running text, and most belong to one language. That needs no
// dependency and no model, and costs about 25 µs on a doc of 4 KB.

// proseLangs are the languages told apart, as lang tags. Chromium's
// hyphenation data has rules for each. English comes first: it
// is the page's own language.
var proseLangs = [...]string{"en", "sv", "da", "nb", "de", "nl", "fr", "es", "it", "pt"}

const langEnglish = 0

// commonWords lists each language's commonest words, in the order of
// proseLangs. Words of one letter are left out: a doc about code is full
// of them. A word common in several languages is listed under each, so it
// favours none.
var commonWords = [len(proseLangs)]string{
	`the of and to in is that it for was on with as be at by this have from or an not are but had they
	you which we were been their has would what will there if can all her when one more no out so up who
	them some could him into its then two only than other may these do did how my our over such after also
	most where any through just your about because very should each those he she his under`,
	`och att det som en på är av för med till den har de inte om ett han men var jag sig från vi så kan
	man när hon under också efter eller nu sin där vid mot ska skulle kommer ut får finns vara hade alla
	andra mycket än här då sedan över bara in blir upp även vad få två vill detta denna dessa några något
	någon utan mellan genom inom hur vilka vilket samt dock måste används använda varje flera ha du`,
	`og at det en den til er som på de med han af for ikke der var mig sig men et har om vi min havde ham
	hun nu over da fra du ud sin dem os op man hans hvor eller hvad skal selv her alle vil blev kunne ind
	når være dog noget ville deres efter ned skulle denne end dette også under have dig anden meget mod
	disse hvis nogle nogen hos blive mange bliver været sådan kan bruges findes mellem uden flere hvilke
	to bare ved andre siden gennem hvordan må hver jeg så samme ingen hvem både fordi før hvilken få
	samt`,
	`og jeg det at en et den til er som på de med han av ikke der så var meg seg men har om vi min ha
	hadde hun nå over da ved fra du ut sin dem oss opp man kan hans hvor eller hva skal selv her alle vil
	bli ble blitt kunne inn når være noen noe ville deres etter ned skulle denne for deg mot dette disse
	uten hvordan ingen blir samme hvilken hvilke mellom hver hvem hvis både bare enn fordi før mange også
	slik vært siden brukes finnes flere to under andre mye få gjennom må ham hos samt`,
	`der die und in den von zu das mit sich des auf für ist im dem nicht ein eine als auch es an werden
	aus er hat dass sie nach wird bei einer um am sind noch wie einem über einen so zum war haben nur oder
	aber vor zur bis mehr durch man sein wurde sei hatte kann gegen vom können schon wenn habe seine ihre
	dann unter wir soll ich eines zwei diese dieser wieder keine seiner worden will zwischen immer was da
	du alle`,
	`de van het een en in is dat op te zijn met voor niet aan er om ook als dan maar bij of uit nog naar
	door over ze zich wordt tot worden deze heeft die je kan meer geen wel was hij ik dit zo kunnen moet
	hun al werd we hebben onder tegen na reeds wat mijn men zou omdat iets toch veel waren heb mij zij nu
	ons haar zonder wie zelf alles doen hier tussen andere elke wanneer alle had`,
	`de la le et les des en un du une que est pour qui dans par plus pas au sur ne se ce il sont avec son
	aux ont ses mais comme ou si leur elle fait cette être tout nous deux aussi était même sans peut entre
	ces sous après vous bien très dont donc lorsque avoir chaque où ainsi alors votre cela peuvent doit
	sera été quand je tous elles ils avant toute autres`,
	`de la que el en los del se las por un para con no una su al lo como más pero sus le ya fue este ha
	sí porque esta son entre cuando muy sin sobre ser tiene también me hasta hay donde han quien están
	estado desde todo nos durante todos uno les ni contra otros ese eso había ante ellos esto antes algunos
	qué unos otro otras otra él tanto esa estos mucho cual cada puede pueden debe así es está`,
	`di che la il un per in una non sono con si da come le dei nel alla più ha anche lo gli del al delle
	questo ma ed se nella della sul sulla ai tra era essere dal loro ci cui dalla suo sua quando molto fra
	tutti questa hanno può viene stato solo ogni dopo senza sia quale quello tutto altri altre deve degli
	nei alle dell negli possono quindi perché`,
	`de que do da em um para com não uma os no se na por mais as dos como mas ao ele das seu sua ou
	quando muito nos já eu também só pelo pela até isso ela entre depois sem mesmo aos seus quem nas me
	esse eles você essa num nem suas meu às minha numa pelos elas qual nós lhe deles essas esses pelas
	este dele são foi tem ser está pode podem deve cada esta estão sobre todos onde então sendo`,
}

// wordLangs maps a common word to the languages that list it, one bit
// each in the order of proseLangs.
var wordLangs = func() map[string]uint16 {
	m := map[string]uint16{}
	for i, list := range commonWords {
		for _, w := range strings.Fields(list) {
			m[w] |= 1 << i
		}
	}
	return m
}()

// longestCommon is the length in bytes of the longest common word; a
// longer word needs no lookup.
var longestCommon = func() int {
	n := 0
	for w := range wordLangs {
		n = max(n, len(w))
	}
	return n
}()

// langCount holds, for a stretch of prose, its words of two letters or
// more, how many of them each language lists as common, and how many each
// pair of languages both list.
type langCount struct {
	words int32
	hits  [len(proseLangs)]int32
	both  [len(proseLangs)][len(proseLangs)]int32
}

func (c *langCount) add(o *langCount) {
	c.words += o.words
	for i, n := range o.hits {
		c.hits[i] += n
		for j, m := range o.both[i] {
			c.both[i][j] += m
		}
	}
}

// hit counts one common word for the languages in mask.
func (c *langCount) hit(mask uint16) {
	for m := mask; m != 0; m &= m - 1 {
		i := bits.TrailingZeros16(m)
		c.hits[i]++
		for rest := m & (m - 1); rest != 0; rest &= rest - 1 {
			c.both[i][bits.TrailingZeros16(rest)]++
		}
	}
}

// count adds the words of text. A word is a run of letters, so an
// apostrophe or a hyphen ends one, and case does not matter.
func (c *langCount) count(text []byte) {
	var folded [64]byte
	for i := 0; i < len(text); {
		start, runes := i, 0
		plain := true // only a to z, as a common word is spelled
		for i < len(text) {
			b := text[i]
			if b >= 'a' && b <= 'z' {
				i++
			} else if b >= 'A' && b <= 'Z' {
				plain = false
				i++
			} else if b < utf8.RuneSelf {
				break
			} else if r, size := utf8.DecodeRune(text[i:]); unicode.IsLetter(r) {
				plain = false
				i += size
			} else {
				break
			}
			runes++
		}
		word := text[start:i]
		if runes == 0 {
			// Not a letter: step over it.
			size := 1
			if text[i] >= utf8.RuneSelf {
				_, size = utf8.DecodeRune(text[i:])
			}
			i += size
			continue
		}
		if runes < 2 {
			continue
		}
		c.words++
		if len(word) > longestCommon {
			continue
		}
		if !plain {
			// At most longestCommon runes of up to four bytes each.
			low := folded[:0]
			for _, r := range string(word) {
				low = utf8.AppendRune(low, unicode.ToLower(r))
			}
			word = low
		}
		if mask := wordLangs[string(word)]; mask != 0 {
			c.hit(mask)
		}
	}
}

// lang is the language the counts name, as an index into proseLangs, or
// -1 when they name none. The leader needs three common words, and one
// word in five must be common to it. Against each other language, only
// the words one of the two lists and the other does not tell them apart:
// the leader needs two such words more than twice the other's. So Danish
// and Norwegian, which share most common words, are told by the few they
// do not share, and a list of names in several languages names none.
func (c *langCount) lang() int {
	best := 0
	for i, n := range c.hits {
		if n > c.hits[best] {
			best = i
		}
	}
	top := c.hits[best]
	if top < 3 || top*5 < c.words {
		return -1
	}
	for i, n := range c.hits {
		if i == best {
			continue
		}
		both := c.both[min(i, best)][max(i, best)]
		if top-both < 2*(n-both)+2 {
			return -1
		}
	}
	return best
}

// markLang gives each block of the doc that is not in English its
// language as a lang attribute. A block too short to tell, such as a
// heading, takes the language of the doc as a whole, and a doc that
// cannot be told is English, as the page says. Code and raw HTML are not
// prose: they are neither counted nor marked.
func markLang(doc ast.Node, src []byte) {
	// A block keeps only the language its own words name; the counts,
	// about 500 bytes, are reused from block to block.
	type block struct {
		node ast.Node
		lang int
	}
	var blocks []block
	var whole, c langCount
	var collect func(parent ast.Node)
	collect = func(parent ast.Node) {
		for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
			switch n.Kind() {
			case ast.KindParagraph, ast.KindHeading, ast.KindBlockquote, ast.KindList, east.KindTable, east.KindDefinitionList:
				c = langCount{}
				countProse(&c, n, src)
				whole.add(&c)
				blocks = append(blocks, block{n, c.lang()})
			case east.KindFootnoteList, east.KindFootnote:
				// Neither writes attributes; the blocks inside do.
				collect(n)
			}
		}
	}
	collect(doc)
	docLang := max(whole.lang(), langEnglish)
	for _, b := range blocks {
		l := b.lang
		if l < 0 {
			l = docLang
		}
		if l != langEnglish {
			b.node.SetAttributeString("lang", []byte(proseLangs[l]))
		}
	}
}

// countProse counts the words a reader reads as prose below n: text, but
// no code and no bare URL.
func countProse(c *langCount, n ast.Node, src []byte) {
	ast.Walk(n, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.CodeSpan:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			c.count(n.Segment.Value(src))
		case *ast.String:
			c.count(n.Value)
		}
		return ast.WalkContinue, nil
	})
}
