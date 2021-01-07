// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// profile contains information about a user.
type profile struct {
	User  string // screen name (without '@')
	Name  string // full name
	Icon  string // small (48x48) favicon URL
	Image string // large (200x200 or 400x400) avatar URL
}

func (p *profile) displayName() string {
	return fmt.Sprintf("%s (@%s)", p.Name, p.User)
}

// tweet describes a single tweet.
type tweet struct {
	ID         int64
	Href       string // absolute URL to tweet
	User       string // screen name (without '@')
	Name       string // full name
	Time       time.Time
	Content    string   // HTML content
	Text       string   // text from content
	ReplyUsers []string // empty if not reply (without '@')
}

func (t *tweet) displayName() string {
	return fmt.Sprintf("%s (@%s)", t.Name, t.User)
}

func (t *tweet) reply() bool {
	// For reasons that I don't understand, the first messages in threads sometimes show up as
	// self-replies. It seems more sensible to treat these as non-replies.
	return len(t.ReplyUsers) > 0 && (len(t.ReplyUsers) > 1 || t.ReplyUsers[0] != t.User)
}

type parseOptions struct {
	simplify bool
}

// parseTimeline reads an HTML document containing a Twitter timeline from r and returns its tweets.
func parseTimeline(r io.Reader, opts parseOptions) (profile, []tweet, error) {
	var prof profile
	root, err := html.Parse(r)
	if err != nil {
		return prof, nil, err
	}
	col := findFirstNode(root, matchFunc("div", "data-testid=primaryColumn"))
	if col == nil {
		return prof, nil, errors.New("didn't find primary column")
	}

	if prof, err = parseProfile(col); err != nil {
		return prof, nil, fmt.Errorf("failed parsing profile: %v", err)
	}

	var tweets []tweet
	for i, tn := range findNodes(col, matchFunc("div", "data-testid=tweet")) {
		tw, err := parseTweet(tn, prof.User, opts)
		if err != nil {
			var id string
			if tw.ID > 0 {
				id = fmt.Sprintf("%d", tw.ID)
			} else {
				id = fmt.Sprintf("at index %d", i)
			}
			return prof, nil, fmt.Errorf("failed parsing tweet %s: %v", id, err)
		}
		tweets = append(tweets, tw)
	}

	return prof, tweets, nil
}

// Matches the size suffix on the end of a profile image, e.g. "_200x200.jpg".
var imgSizeRegexp = regexp.MustCompile(`_\d+x\d+\.jpg$`)

// parseProfile parses profile data from the supplied primary column from a timeline page.
func parseProfile(n *html.Node) (profile, error) {
	var pr profile

	// TODO: Should check that the username matches, but we don't pass it in.
	un := findFirstNode(n, func(n *html.Node) bool {
		return isText(n) && len(n.Data) > 1 && n.Data[0] == '@'
	})
	if un == nil {
		return pr, errors.New("didn't find username")
	}
	pr.User = un.Data[1:]

	if un.Parent == nil || un.Parent.Parent == nil || un.Parent.Parent.Parent == nil {
		return pr, errors.New("didn't find full name")
	}
	pr.Name = getText(un.Parent.Parent.Parent.PrevSibling, false)

	img := findFirstNode(n, func(n *html.Node) bool {
		return isElement(n, "img") && strings.Contains(getAttr(n, "src"), "/profile_images/")
	})
	if img == nil {
		return pr, errors.New("didn't find profile image")
	}
	pr.Image = imgSizeRegexp.ReplaceAllLiteralString(getAttr(img, "src"), "_400x400.jpg")
	pr.Icon = imgSizeRegexp.ReplaceAllLiteralString(pr.Image, "_normal.jpg")

	return pr, nil
}

// parseTweet parses a single tweet from the supplied tweet div.
func parseTweet(n *html.Node, timelineUser string, opts parseOptions) (tweet, error) {
	var tw tweet
	if n.FirstChild == nil || n.FirstChild.NextSibling == nil {
		return tw, errors.New("no right column")
	}
	main := n.FirstChild.NextSibling // first child is left column with profile photo

	// Emoji are (usually?) represented by divs containing img tags, so replace all
	// that garbage with text nodes containing the actual emoji.
	fixEmoji(main)

	head := main.FirstChild
	if head == nil {
		return tw, errors.New("no header")
	}

	// The timestamp is stored in the "datetime" attribute of a <time> element.
	tm := findFirstNode(head, matchFunc("time", "datetime"))
	if tm == nil {
		return tw, errors.New("failed finding time")
	}
	var err error
	if tw.Time, err = time.Parse(time.RFC3339, getAttr(tm, "datetime")); err != nil {
		return tw, err
	}

	// The <time> element is wrapped in a link to the tweet itself.
	ln := tm.Parent
	if !isElement(ln, "a") {
		return tw, errors.New("failed finding link")
	}
	if err != nil {
		return tw, err
	}
	tw.Href = absoluteURL(getAttr(ln, "href"))

	// The ID is the last component of the URL.
	if tw.ID, err = strconv.ParseInt(path.Base(tw.Href), 10, 64); err != nil {
		return tw, fmt.Errorf("failed parsing ID: %v", err)
	}

	// Just look for the first text node starting with "@" to get the user.
	un := findFirstNode(head, func(n *html.Node) bool {
		return isText(n) && len(n.Data) > 1 && n.Data[0] == '@'
	})
	if un == nil {
		return tw, errors.New("didn't find username")
	}
	tw.User = un.Data[1:]

	// The full name lives in a sibling of the username text node's great-grandparent.
	if un.Parent == nil || un.Parent.Parent == nil || un.Parent.Parent.Parent == nil {
		return tw, errors.New("didn't find full name")
	}
	tw.Name = getText(un.Parent.Parent.Parent.PrevSibling, false)

	body := head.NextSibling
	if body == nil {
		return tw, errors.New("no body")
	}

	// Within the body, there's:
	// - an optional div containing "Replying to ..."
	// - a div containing the tweet text (possibly empty)
	// - a div containing an image or retweet (possibly empty)
	// - a div containing info about replies, retweets, and likes
	var children []*html.Node
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		children = append(children, c)
	}
	var text, embed *html.Node
	switch len(children) {
	case 3:
		text = children[0]
		embed = children[1]
	case 4:
		// Extract the replied-to users from the first child.
		for _, n := range findNodes(children[0], func(n *html.Node) bool {
			return isText(n) && len(n.Data) > 1 && n.Data[0] == '@'
		}) {
			tw.ReplyUsers = append(tw.ReplyUsers, n.Data[1:])
		}
		text = children[1]
		embed = children[2]
	default:
		return tw, fmt.Errorf("body contains %d children; want 3 or 4", len(children))
	}

	content := &html.Node{Type: html.ElementNode, DataAtom: atom.Div, Data: "div"}

	// If this is a retweet, add an attribution link at the top.
	if tw.User != timelineUser {
		link := &html.Node{
			Type:     html.ElementNode,
			DataAtom: atom.A,
			Data:     "a",
			Attr:     []html.Attribute{{Key: "href", Val: tw.Href}},
		}
		link.AppendChild(&html.Node{Type: html.TextNode, Data: fmt.Sprintf("%s (@%s)", tw.Name, tw.User)})
		bold := &html.Node{Type: html.ElementNode, DataAtom: atom.B, Data: "b"}
		bold.AppendChild(link)
		content.AppendChild(bold)
		content.AppendChild(&html.Node{Type: html.ElementNode, DataAtom: atom.Br, Data: "br"})
	}

	body.RemoveChild(text)
	content.AppendChild(text)

	if embed.FirstChild != nil {
		content.AppendChild(&html.Node{Type: html.ElementNode, DataAtom: atom.Hr, Data: "hr"})
		content.AppendChild(&html.Node{Type: html.ElementNode, DataAtom: atom.Br, Data: "br"})
		body.RemoveChild(embed)
		improveQuoteTweetHeader(embed)
		improveLinkCard(embed)
		content.AppendChild(embed)
	}

	fixVideos(content)
	rewriteRelativeLinks(content)
	inlineUserLinks(content)
	addLineBreaks(content)

	// class attributes are meaningless since we aren't using the original stylesheet.
	deleteAttr(content, "class")
	if opts.simplify {
		simplifyContent(content)
	}

	var b bytes.Buffer
	if err := html.Render(&b, content); err != nil {
		return tw, fmt.Errorf("failed rendering text: %v", err)
	}
	tw.Content = b.String()
	tw.Text = getText(content, true)

	return tw, nil
}

// Used by fixEmoji to extract code point from e.g.
// "https://abs-0.twimg.com/emoji/v2/svg/1f449.svg".
var emojiRegexp = regexp.MustCompile(`^https://.*/emoji/v2/svg/([0-9a-f]+)\.svg$`)

// fixEmoji emoji images with text nodes containing the emoji themselves.
func fixEmoji(root *html.Node) {
	// Emoji are placed within divs for no good reason as far as I can tell. We need
	// to replace the outer divs so that we don't start a new block in the HTML.
	for _, n := range findNodes(root, func(n *html.Node) bool {
		return isElement(n, "div") && getAttr(n, "style") == "height: 1.2em;" && getAttr(n, "aria-label") != ""
	}) {
		img := findFirstNode(n, func(n *html.Node) bool {
			return isElement(n, "img") && emojiRegexp.MatchString(getAttr(n, "src"))
		})
		if img == nil {
			continue
		}
		u := emojiRegexp.FindStringSubmatch(getAttr(img, "src"))[1]
		v, err := strconv.ParseUint(u, 16, 64)
		if err != nil {
			debugf("Failed getting code point from %q: %v", u, err)
			continue
		}
		if v > unicode.MaxRune {
			debugf("Invalid code point U+%X in %q", v, err)
			continue
		}
		// Replace the outer div with a text node containing the actual emoji.
		*n = html.Node{Type: html.TextNode, Data: string(rune(v))}
	}
}

// addLineBreaks splits text nodes under n on newlines and inserts <br> tags.
func addLineBreaks(n *html.Node) {
	for _, tn := range findNodes(n, func(n *html.Node) bool {
		return isText(n) && strings.Contains(n.Data, "\n")
	}) {
		parent := tn.Parent
		next := tn.NextSibling
		parent.RemoveChild(tn)

		parts := strings.Split(tn.Data, "\n")
		for i, p := range parts {
			if p != "" {
				tn := &html.Node{Type: html.TextNode, Data: p}
				parent.InsertBefore(tn, next)
				next = tn.NextSibling
			}
			if i < len(parts)-1 {
				br := &html.Node{Type: html.ElementNode, DataAtom: atom.Br, Data: "br"}
				parent.InsertBefore(br, next)
				next = br.NextSibling

				tn := &html.Node{Type: html.TextNode, Data: "\n"}
				parent.InsertBefore(tn, next)
				next = tn.NextSibling
			}
		}
	}
}

// rewriteRelativeLinks rewrites all relative links in n to be absolute.
func rewriteRelativeLinks(n *html.Node) {
	for _, link := range findNodes(n, matchFunc("a")) {
		for i, a := range link.Attr {
			if a.Key == "href" {
				link.Attr[i].Val = absoluteURL(a.Val)
			}
		}
	}
}

// inlineUserLinks converts divs wrapping user links in a tweet into spans.
func inlineUserLinks(n *html.Node) {
	for _, link := range findNodes(n, func(n *html.Node) bool {
		// Match links containing a single text node with a username in it.
		return isElement(n, "a") &&
			n.FirstChild != nil && n.FirstChild == n.LastChild && isText(n.FirstChild) &&
			strings.HasPrefix(n.FirstChild.Data, "@") && !strings.Contains(n.FirstChild.Data, " ") &&
			isElement(n.Parent, "span") && isElement(n.Parent.Parent, "div")
	}) {
		div := link.Parent.Parent
		div.Data = "span"
		div.DataAtom = atom.Span
	}
}

// improveQuoteTweetHeader looks for a quoted tweet header in n, an embed.
// If it finds one, it replaces it with a single text node containing its text contents.
func improveQuoteTweetHeader(n *html.Node) {
	// Look for a timestamp to try to identify a quoted tweet header.
	tn := findFirstNode(n, matchFunc("time"))
	if tn == nil || !isElement(tn.Parent, "span") || !isElement(tn.Parent.Parent, "div") ||
		!isElement(tn.Parent.Parent.Parent, "div") {
		return
	}

	// It looks like Twitter doesn't give us the link to the quoted tweet, unfortunately.
	// Just merge all the text so it isn't spread across multiple divs. Prepend a space so
	// the it won't be flush against the profile image -- Feedly strips most (all?) styling.
	div := tn.Parent.Parent.Parent
	s := " " + getText(div, true)

	// Find the profile image and detach it so we can add it later.
	img := findFirstNode(div, func(n *html.Node) bool {
		return isElement(n, "img") && strings.Contains(getAttr(n, "src"), "/profile_images/")
	})
	if img != nil {
		img.Parent.RemoveChild(img)
	}

	for div.FirstChild != nil {
		div.RemoveChild(div.FirstChild)
	}
	if img != nil {
		div.AppendChild(img)
	}
	bold := &html.Node{Type: html.ElementNode, DataAtom: atom.B, Data: "b"}
	bold.AppendChild(&html.Node{Type: html.TextNode, Data: s})
	div.AppendChild(bold)

	// Also get rid of useless text.
	for _, n := range findNodes(n, func(n *html.Node) bool {
		return isText(n) && (n.Data == "Quote Tweet" ||
			n.Data == "Show this poll" || n.Data == "Show this thread")
	}) {
		n.Data = ""
	}
}

// improveLinkCard looks for a link card in n and improves its styling.
func improveLinkCard(n *html.Node) {
	cn := findFirstNode(n, func(n *html.Node) bool {
		if !isElement(n, "div") {
			return false
		}
		id := getAttr(n, "data-testid")
		return id == "card.layoutSmall.detail" || id == "card.layoutLarge.detail"
	})
	if cn == nil {
		return
	}

	// Cards appear to contain three div children: a title, description, and domain.
	var children []*html.Node
	for n := cn.FirstChild; n != nil; n = n.NextSibling {
		children = append(children, n)
	}
	if len(children) != 3 {
		return
	}

	// Reparent the title text under a <b> element.
	if title := findFirstNode(children[0], isText); title != nil {
		bold := &html.Node{Type: html.ElementNode, DataAtom: atom.B, Data: "b"}
		title.Parent.InsertBefore(bold, title)
		title.Parent.RemoveChild(title)
		bold.AppendChild(title)
	}

	// Reparent the domain under an <i> element.
	if domain := findFirstNode(children[2], isText); domain != nil {
		italic := &html.Node{Type: html.ElementNode, DataAtom: atom.I, Data: "i"}
		domain.Parent.InsertBefore(italic, domain)
		domain.Parent.RemoveChild(domain)
		italic.AppendChild(domain)
	}
}

// fixVideos tries to improve <video> elements under n, an embed.
// The "controls" attribute is added to playable (i.e. non-blob) elements,
// and <img> tags containing screenshots are removed.
func fixVideos(n *html.Node) {
	for _, v := range findNodes(n, matchFunc("video")) {
		src := getAttr(v, "src")
		if !strings.HasPrefix(src, "blob:") {
			v.Attr = append(v.Attr, html.Attribute{Key: "controls"})
		}
		if poster := getAttr(v, "poster"); poster != "" {
			for _, img := range findNodes(n, matchFunc("img", "src="+poster)) {
				img.Parent.RemoveChild(img)
			}
		}
	}
}

// simplifyContent removes probably-useless elements and styling from within root.
func simplifyContent(root *html.Node) {
	// Some (most?) RSS readers ignore style attributes.
	// It's also good to drop weird "padding-bottom: 48.3333%;" styles on
	// divs preceding embedded images.
	deleteAttr(root, "style")

	deleteAttr(root, "aria-hidden")    // set on links that will no longer be hidden
	deleteAttr(root, "data-focusable") // set on links
	deleteAttr(root, "data-testid")    // useful earlier for indentifying stuff, but not needed now
	deleteAttr(root, "draggable")      // seems to be set on images, but default is already true
	deleteAttr(root, "role")           // redundant role="link" attributes on links

	// Also drop SVG elements, since they might not get rendered and show
	// up with a huge size if they are.
	for _, n := range findNodes(root, matchFunc("svg")) {
		n.Parent.RemoveChild(n)
	}
}
