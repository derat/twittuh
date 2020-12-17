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

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// profile contains information about a user.
type profile struct {
	user  string // screen name (without '@')
	name  string // full name
	icon  string // small (48x48) favicon URL
	image string // large (200x200 or 400x400) avatar URL
}

func (p *profile) displayName() string {
	return fmt.Sprintf("%s (@%s)", p.name, p.user)
}

// tweet describes a single tweet.
type tweet struct {
	id      int64
	href    string    // absolute URL to tweet
	user    string    // screen name (without '@')
	name    string    // full name
	time    time.Time // approximate (Twitter just gives us age)
	content string    // HTML content
	text    string    // text from content

	replyUsers []string // empty if not reply (without '@')
}

func (t *tweet) displayName() string {
	return fmt.Sprintf("%s (@%s)", t.name, t.user)
}

func (t *tweet) reply() bool {
	// For reasons that I don't understand, the first messages in threads sometimes show up as
	// self-replies. It seems more sensible to treat these as non-replies.
	return len(t.replyUsers) > 0 && (len(t.replyUsers) > 1 || t.replyUsers[0] != t.user)
}

// parseTimeline reads an HTML document containing a Twitter timeline from r and returns its tweets.
func parseTimeline(r io.Reader) (profile, []tweet, error) {
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
	for _, tn := range findNodes(col, matchFunc("div", "data-testid=tweet")) {
		tw, err := parseTweet(tn)
		if err != nil {
			return prof, nil, fmt.Errorf("failed parsing tweet %d", tw.id)
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
		return n.Type == html.TextNode && len(n.Data) > 1 && n.Data[0] == '@'
	})
	if un == nil {
		return pr, errors.New("didn't find username")
	}
	pr.user = un.Data[1:]

	if un.Parent == nil || un.Parent.Parent == nil || un.Parent.Parent.Parent == nil {
		return pr, errors.New("didn't find full name")
	}
	pr.name = getText(un.Parent.Parent.Parent.PrevSibling)

	img := findFirstNode(n, func(n *html.Node) bool {
		return isElement(n, "img") && strings.Contains(getAttr(n, "src"), "/profile_images/")
	})
	if img == nil {
		return pr, errors.New("didn't find profile image")
	}
	pr.image = imgSizeRegexp.ReplaceAllLiteralString(getAttr(img, "src"), "_400x400.jpg")
	pr.icon = imgSizeRegexp.ReplaceAllLiteralString(pr.image, "_normal.jpg")

	return pr, nil
}

// parseTweet parses a single tweet from the supplied tweet div.
func parseTweet(n *html.Node) (tweet, error) {
	var tw tweet
	if n.FirstChild == nil || n.FirstChild.NextSibling == nil {
		return tw, errors.New("no right column")
	}
	main := n.FirstChild.NextSibling // first child is left column with profile photo

	// Replace annoying emoji divs with text nodes containing the emoji themselves.
	for _, n := range findNodes(main, func(n *html.Node) bool {
		return isElement(n, "div") && getAttr(n, "style") == "height: 1.2em;"
	}) {
		*n = html.Node{Type: html.TextNode, Data: getAttr(n, "aria-label")}
	}

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
	if tw.time, err = time.Parse(time.RFC3339, getAttr(tm, "datetime")); err != nil {
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
	tw.href = absoluteURL(getAttr(ln, "href"))

	// The ID is the last component of the URL.
	if tw.id, err = strconv.ParseInt(path.Base(tw.href), 10, 64); err != nil {
		return tw, fmt.Errorf("failed parsing ID: %v", err)
	}

	// Just look for the first text node starting with "@" to get the user.
	un := findFirstNode(head, func(n *html.Node) bool {
		return n.Type == html.TextNode && len(n.Data) > 1 && n.Data[0] == '@'
	})
	if un == nil {
		return tw, errors.New("didn't find username")
	}
	tw.user = un.Data[1:]

	// The full name lives in a sibling of the username text node's great-grandparent.
	// Twitter seems to serve Emoji as images here, so we unfortunately don't get them.
	if un.Parent == nil || un.Parent.Parent == nil || un.Parent.Parent.Parent == nil {
		return tw, errors.New("didn't find full name")
	}
	tw.name = getText(un.Parent.Parent.Parent.PrevSibling)

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
			return n.Type == html.TextNode && len(n.Data) > 1 && n.Data[0] == '@'
		}) {
			tw.replyUsers = append(tw.replyUsers, n.Data[1:])
		}
		text = children[1]
		embed = children[2]
	default:
		return tw, fmt.Errorf("body contains %d children; want 3 or 4", len(children))
	}

	content := &html.Node{Type: html.ElementNode, DataAtom: atom.Div, Data: "div"}
	body.RemoveChild(text)
	content.AppendChild(text)

	if embed.FirstChild != nil {
		content.AppendChild(&html.Node{Type: html.ElementNode, DataAtom: atom.Hr, Data: "hr"})
		body.RemoveChild(embed)
		content.AppendChild(embed)
	}

	deleteAttr(content, "class")
	rewriteRelativeLinks(content)
	addLineBreaks(content)

	var b bytes.Buffer
	if err := html.Render(&b, content); err != nil {
		return tw, fmt.Errorf("failed rendering text: %v", err)
	}
	tw.content = b.String()
	tw.text = getText(content)

	return tw, nil
}

// addLineBreaks splits text nodes under n on newlines and inserts <br> tags.
func addLineBreaks(n *html.Node) {
	for _, tn := range findNodes(n, func(n *html.Node) bool {
		return n.Type == html.TextNode && strings.Contains(n.Data, "\n")
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
