// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	defaultScheme = "https"
	defaultHost   = "twitter.com"
)

// tweet describes a single tweet.
type tweet struct {
	id      int64
	href    string    // absolute URL to tweet
	name    string    // full name
	user    string    // username (without '@')
	time    time.Time // approximate (Twitter just gives us age)
	content string    // HTML content
	text    string    // text from content

	replyUsers []string // empty if not reply (without '@')
}

func (t *tweet) displayName() string {
	return fmt.Sprintf("%s (@%s)", t.name, t.user)
}

func (t *tweet) reply() bool {
	return len(t.replyUsers) > 0
}

// parse reads an HTML document containing a Twitter timeline from r and returns its tweets.
func parse(r io.Reader, ft *fetcher) ([]tweet, error) {
	root, err := html.Parse(r)
	if err != nil {
		return nil, err
	}
	p := parser{fetcher: ft}
	if err := p.proc(root); err != nil {
		return nil, err
	}
	return p.tweets, nil
}

type parser struct {
	fetcher  *fetcher
	timeline bool    // true while in timeline div
	curTweet *tweet  // in-progress tweet
	tweets   []tweet // completed tweets
}

func (p *parser) proc(n *html.Node) error {
	switch {
	case !p.timeline:
		if isElement(n, "div") && hasClass(n, "timeline") {
			p.timeline = true
			defer func() { p.timeline = false }()
		}
	case p.curTweet == nil:
		if isElement(n, "table") && hasClass(n, "tweet") {
			href, err := rewriteURL(getAttr(n, "href"))
			if err != nil {
				return err
			}
			p.curTweet = &tweet{href: href}
			defer func() {
				p.tweets = append(p.tweets, *p.curTweet)
				p.curTweet = nil
			}()
		}
	// In the remaining cases, we're inside a tweet.
	case isElement(n, "strong") && hasClass(n, "fullname"):
		p.curTweet.name = cleanText(getText(n))
		return nil // skip contents
	case isElement(n, "div") && hasClass(n, "username"):
		if hasClass(n, "tweet-reply-context") {
			// The username(s) appear inside <a> elements nested under the div.
			for _, a := range findNodes(n, func(n *html.Node) bool { return isElement(n, "a") }) {
				p.curTweet.replyUsers = append(p.curTweet.replyUsers, bareUser(cleanText(getText(a))))
			}
		} else {
			p.curTweet.user = bareUser(cleanText(getText(n)))
		}
		return nil // skip contents
	case isElement(n, "td") && hasClass(n, "timestamp"):
		var err error
		s := strings.TrimSpace(getText(n))
		if p.curTweet.time, err = parseTime(s, time.Now()); err != nil {
			return fmt.Errorf("bad time %q: %v", s, err)
		}
		return nil // skip contents
	case isElement(n, "div") && hasClass(n, "tweet-text"):
		var err error
		if p.curTweet.id, err = strconv.ParseInt(getAttr(n, "data-id"), 10, 64); err != nil {
			return fmt.Errorf("failed parsing ID: %v", err)
		}
		// Extract a plain-text version of the tweet first.
		p.curTweet.text = cleanText(getText(n))

		addEmbedded(n, p.fetcher)
		var b bytes.Buffer
		if err := html.Render(&b, n); err != nil {
			return fmt.Errorf("failed rendering text: %v", err)
		}
		p.curTweet.content = b.String()
		return nil // skip contents
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := p.proc(c); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) getImageEmbed(n *html.Node) (string, error) {
	// Look for a link to the page containing the image.
	embeds := findNodes(n, func(n *html.Node) bool {
		return isElement(n, "a") && getAttr(n, "data-pre-embedded") == "true"
	})
	if len(embeds) == 0 {
		return "", nil
	}
	us := getAttr(embeds[0], "data-url")
	if us == "" {
		return "", errors.New("no data-url attribute")
	}
	u, err := url.Parse(us)
	if err != nil {
		return "", err
	}

	// Rewrite host to get simple HTML version of image page.
	if u.Host == "twitter.com" {
		u.Host = "mobile.twitter.com"
	}
	if u.Host != "mobile.twitter.com" || !strings.Contains(u.Path, "/photo/") {
		return "", nil
	}

	// Download and parse the image page to look for a <div class="media"> with an <img> inside of it.
	b, err := p.fetcher.fetch(u.String(), true /* useCache */)
	if err != nil {
		return "", err
	}
	root, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	media := findNodes(root, func(n *html.Node) bool { return isElement(n, "div") && hasClass(n, "media") })
	if len(media) == 0 {
		return "", nil
	}
	imgs := findNodes(media[0], func(n *html.Node) bool { return isElement(n, "img") })
	if len(imgs) == 0 {
		return "", nil
	}
	return getAttr(imgs[0], "src"), nil
}

// findNodes returns node within the tree rooted at n for which f returns true.
func findNodes(n *html.Node, f func(*html.Node) bool) []*html.Node {
	var ns []*html.Node

	if f(n) {
		ns = append(ns, n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		ns = append(ns, findNodes(c, f)...)
	}
	return ns
}

// isElement returns true if n is an HTML element with the supplied tag type.
func isElement(n *html.Node, tag string) bool {
	return n.Type == html.ElementNode && n.Data == tag
}

// hasClass returns true if n's "class" attribute contains class.
func hasClass(n *html.Node, class string) bool {
	for _, v := range strings.Fields(getAttr(n, "class")) {
		if v == class {
			return true
		}
	}
	return false
}

// getAttr returns the first occurrence of the named attribute from n.
// An empty string is returned if the attribute isn't present.
func getAttr(n *html.Node, attr string) string {
	for _, a := range n.Attr {
		if a.Key == attr {
			return a.Val
		}
	}
	return ""
}

// getText concatenates all text content in and under n.
func getText(n *html.Node) string {
	var text string
	if n.Type == html.TextNode {
		text += n.Data
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		text += getText(c)
	}
	return text
}

var spaceRegexp = regexp.MustCompile(`\s+`)

// cleanText trims whitespace from the beginning and end of s and condenses repeated whitespace.
func cleanText(s string) string {
	s = strings.TrimSpace(s)
	s = spaceRegexp.ReplaceAllString(s, " ")
	return s
}

// TODO: I'm not sure that seconds or days are used.
var durationRegexp = regexp.MustCompile(`^(\d+)([smhd])$`)

// parseTime parses a Twitter-supplied "timestamp".
// These can take a bunch of forms, e.g. "23m", "2h", "Jul 9", etc.
func parseTime(s string, now time.Time) (time.Time, error) {
	if ms := durationRegexp.FindStringSubmatch(s); ms != nil {
		quant, err := strconv.Atoi(ms[1])
		if err != nil {
			return time.Time{}, errors.New("bad quantity")
		}
		var units time.Duration
		switch ms[2] {
		case "s":
			units = time.Second
		case "m":
			units = time.Minute
		case "h":
			units = time.Hour
		case "d":
			units = 24 * time.Hour // busted for DST, but what can you do
		default:
			return time.Time{}, errors.New("bad units") // shouldn't happen
		}
		return now.Add(-1 * time.Duration(quant) * units), nil
	}

	if t, err := time.Parse("Jan 2", s); err == nil {
		return t, nil
	}

	return time.Time{}, errors.New("unknown format")
}

// rewriteURL rewrites s to be an absolute URL served by Twitter.
// If s is already absolute, it is returned unchanged.
func rewriteURL(s string) (string, error) {
	if s == "" {
		return "", errors.New("empty URL")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", err
	}
	if u.IsAbs() {
		return s, nil
	}
	u.Scheme = defaultScheme
	u.Host = defaultHost
	return u.String(), nil
}

// addEmbedded adds embedded content that's linked within n.
func addEmbedded(n *html.Node, ft *fetcher) {
	// Look for links to image pages: <a data-pre-embedded="true" data-url="...">.
	for _, link := range findNodes(n, func(n *html.Node) bool {
		return isElement(n, "a") && getAttr(n, "data-pre-embedded") == "true"
	}) {
		url, err := getImageURL(getAttr(link, "data-url"), ft)
		if err != nil || url == "" {
			continue // TODO: Log error.
		}
		// Replace the link's children (formerly the photo page URL) with an <img> tag.
		for link.LastChild != nil {
			link.RemoveChild(link.LastChild)
		}
		link.AppendChild(&html.Node{
			Type:     html.ElementNode,
			DataAtom: atom.Img,
			Data:     "img",
			Attr:     []html.Attribute{html.Attribute{Key: "src", Val: url}},
		})
		// Make sure the link isn't displayed inline.
		link.Attr = append(link.Attr, html.Attribute{Key: "style", Val: "display:block"})
	}

	// TODO: Also handle embedded tweets.
}

// getImageURL attempts to extract the underlying URL to the image from the supplied photo
// page URL, e.g. "https://twitter.com/biff_tannen/status/12813232543132445323/photo/1".
// If the URL is not a photo page, an empty string is returned.
func getImageURL(u string, ft *fetcher) (string, error) {
	// Check if we got a photo page URL.
	url, err := url.Parse(u)
	if err != nil {
		return "", nil
	}
	if url.Host == "twitter.com" {
		url.Host = "mobile.twitter.com" // get simple HTML page
	} else if url.Host != "mobile.twitter.com" {
		return "", nil
	}
	if !strings.Contains(url.Path, "/photo/") {
		return "", nil
	}

	// Download and parse the image page to look for a <div class="media"> with an <img> inside of it.
	b, err := ft.fetch(url.String(), true /* useCache */)
	if err != nil {
		return "", err
	}
	if root, err := html.Parse(bytes.NewReader(b)); err != nil {
		return "", err
	} else if media := findNodes(root, func(n *html.Node) bool { return isElement(n, "div") && hasClass(n, "media") }); len(media) == 0 {
		return "", errors.New("didn't find media div")
	} else if imgs := findNodes(media[0], func(n *html.Node) bool { return isElement(n, "img") }); len(imgs) == 0 {
		return "", errors.New("didn't find image")
	} else {
		return getAttr(imgs[0], "src"), nil
	}
}
