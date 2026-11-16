package util

import (
	"bytes"
	"html"
	"regexp"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	goldmarkutil "github.com/yuin/goldmark/util"
	nethtml "golang.org/x/net/html"
)

// Mention represents a parsed @mention from markdown content.
type Mention struct {
	Type string // "member", "agent", "squad", "issue", or "all"
	ID   string // user_id, agent_id, issue_id, or "all"
	// Label is the visible mention text without the "@" prefix (M-359: the
	// `@agent#tier` suffix rides the label so the wire stays stable).
	Label string
}

// MentionRe matches [@Label](mention://type/id) or [Label](mention://issue/id) in markdown.
// The @ prefix is optional to support issue mentions which use [MUL-123](mention://issue/...).
// Uses .+? (non-greedy) instead of [^\]]* so labels containing square brackets
// (e.g. "David[TF]") are matched correctly — the ](mention:// anchor is specific
// enough to prevent over-matching.
var MentionRe = regexp.MustCompile(`\[@?(.+?)\]\(mention://(member|agent|squad|issue|all)/([0-9a-fA-F-]+|all)\)`)

var canonicalMentionRe = regexp.MustCompile("^" + MentionRe.String() + "$")

// IsMentionAll returns true if the mention is an @all mention.
func (m Mention) IsMentionAll() bool {
	return m.Type == "all"
}

// ParseMentions extracts deduplicated mentions from markdown content.
// Mentions inside fenced code blocks or inline code spans are ignored — they
// document the mention syntax rather than trigger an agent run. Mentions in
// blockquotes are still returned on purpose.
//
// The walk uses goldmark's CommonMark AST and only considers Link nodes, so
// code spans, fenced blocks (including nested-in-blockquote and CommonMark
// fence-length rules), and multiline code spans are excluded without a
// hand-rolled scanner.
// Each link must also use MentionRe's canonical inline source form and have
// visible label text. Reference links and other Markdown link forms are inert.
func ParseMentions(content string) []Mention {
	source := []byte(content)
	links := &mentionLinkParser{InlineParser: parser.NewLinkParser(), spans: make(map[*ast.Link]text.Segment)}
	inlineParsers := parser.DefaultInlineParsers()
	for i := range inlineParsers {
		if inlineParsers[i].Value == links.InlineParser {
			inlineParsers[i].Value = links
		}
	}
	markdown := parser.NewParser(
		parser.WithBlockParsers(parser.DefaultBlockParsers()...),
		parser.WithInlineParsers(inlineParsers...),
		parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
	)
	doc := markdown.Parse(text.NewReader(source))
	seen := make(map[string]int)
	var result []Mention
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if _, ok := n.(*ast.Image); ok {
			return ast.WalkSkipChildren, nil
		}
		link, ok := n.(*ast.Link)
		if !ok {
			return ast.WalkContinue, nil
		}
		mention, ok := parseMentionLink(link, links.spans[link], source)
		if !ok {
			return ast.WalkContinue, nil
		}
		// M-359: dedup key is type + id + tier slot. `@a#x` vs `@a#y`
		// and plain `@a` vs `@a#x` are distinct triggers and must each
		// run; only the identical mention dedups.
		_, tier, _ := SplitTierSuffix(mention.Label)
		key := mention.Type + ":" + mention.ID + ":" + tier
		if _, dup := seen[key]; dup {
			return ast.WalkContinue, nil
		}
		seen[key] = len(result)
		result = append(result, mention)
		return ast.WalkContinue, nil
	})
	return result
}

// Goldmark links expose their start, but not their end. Record the span while
// its link parser owns the reader, before inline nodes lose that boundary.
type mentionLinkParser struct {
	parser.InlineParser
	spans map[*ast.Link]text.Segment
}

func (p *mentionLinkParser) Parse(parent ast.Node, reader text.Reader, context parser.Context) ast.Node {
	n := p.InlineParser.Parse(parent, reader, context)
	if link, ok := n.(*ast.Link); ok {
		_, end := reader.Position()
		p.spans[link] = text.NewSegment(link.Pos(), end.Start)
	}
	return n
}

func (p *mentionLinkParser) CloseBlock(parent ast.Node, reader text.Reader, context parser.Context) {
	// Preserve the link parser's cleanup of unmatched openers between blocks.
	p.InlineParser.(parser.CloseBlocker).CloseBlock(parent, reader, context)
}

func parseMentionLink(link *ast.Link, span text.Segment, source []byte) (Mention, bool) {
	if link.Reference != nil || len(link.Title) != 0 {
		return Mention{}, false
	}
	raw := span.Value(source)
	m := canonicalMentionRe.FindSubmatchIndex(raw)
	if m == nil {
		return Mention{}, false
	}
	mention := Mention{Type: string(raw[m[4]:m[5]]), ID: string(raw[m[6]:m[7]])}
	if string(link.Destination) != "mention://"+mention.Type+"/"+mention.ID {
		return Mention{}, false
	}

	var label strings.Builder
	hasRawHTML := false
	_ = ast.Walk(link, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if _, ok := n.(*ast.Image); ok {
				return ast.WalkSkipChildren, nil
			}
			if _, ok := n.(*ast.RawHTML); ok {
				hasRawHTML = true
			}
			if node, ok := n.(*ast.Text); ok {
				value := node.Segment.Value(source)
				if !node.IsRaw() {
					value = goldmarkutil.UnescapePunctuations(value)
					label.WriteString(html.UnescapeString(string(value)))
				} else {
					label.Write(value)
				}
			}
		}
		return ast.WalkContinue, nil
	})
	labelText := label.String()
	if hasRawHTML {
		labelText = renderedMentionLabelText(link, source)
	}
	firstVisible := true
	for _, r := range labelText {
		if unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || r == '\u2800' {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			if firstVisible && r == '@' {
				firstVisible = false
				continue
			}
			mention.Label = strings.TrimPrefix(labelText, "@")
			return mention, true
		}
	}
	return Mention{}, false
}

// Raw HTML changes which text survives the frontend's rehype-raw/sanitize
// pipeline. Parse the rendered fragment so script raw-text rules (including
// case, attributes, and self-closing syntax) match HTML rather than Markdown.
// This HTML is only inspected in memory; it is never served or executed.
func renderedMentionLabelText(link *ast.Link, source []byte) string {
	var rendered bytes.Buffer
	markdown := goldmark.New(goldmark.WithRendererOptions(goldmarkhtml.WithUnsafe()))
	if err := markdown.Renderer().Render(&rendered, source, link); err != nil {
		return ""
	}
	doc, err := nethtml.Parse(&rendered)
	if err != nil {
		return ""
	}
	var label strings.Builder
	var collect func(*nethtml.Node)
	collect = func(n *nethtml.Node) {
		// markdownSanitizeSchema strips scripts. parse5 stores template
		// contents in a separate fragment, so sanitization also drops them
		// when unwrapping the template. x/net/html keeps them as children.
		// Other disallowed tags, such as style, are unwrapped, keeping text.
		if n.Type == nethtml.ElementNode && (n.Data == "script" || n.Data == "template") {
			return
		}
		if n.Type == nethtml.TextNode {
			label.WriteString(n.Data)
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			collect(child)
		}
	}
	// Only the generated anchor is the mention label. Raw closing tags can
	// move text outside it; that text must not validate an empty mention.
	for node := range doc.Descendants() {
		if node.Type == nethtml.ElementNode && node.Data == "a" {
			collect(node)
			break
		}
	}
	return label.String()
}

// HasMentionAll returns true if any mention in the slice is an @all mention.
func HasMentionAll(mentions []Mention) bool {
	for _, m := range mentions {
		if m.IsMentionAll() {
			return true
		}
	}
	return false
}

// tierSuffixPattern is the M-350 slug shape ([a-z0-9][a-z0-9-]*, <=40
// chars). A `#...` tail must match it to count as a tier pick; anything
// else (C#, `a#b#c` with a bad tail, trailing `#`) is agent-name text.
var tierSuffixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

const maxTierSuffixLen = 40

// SplitTierSuffix splits an `@agent#tier` label at its last `#` (M-359).
// hasTier is true only when the tail is slug-shaped; matching against real
// tier names happens at resolve time. No `#`, or a non-slug tail, returns
// the label untouched with hasTier=false so legacy names keep working.
func SplitTierSuffix(label string) (agentPart, tierPart string, hasTier bool) {
	hash := strings.LastIndex(label, "#")
	if hash < 0 {
		return label, "", false
	}
	tail := label[hash+1:]
	if len(tail) == 0 || len(tail) > maxTierSuffixLen || !tierSuffixPattern.MatchString(strings.ToLower(tail)) {
		return label, "", false
	}
	return label[:hash], strings.ToLower(tail), true
}
