// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
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
	mobileHost    = "mobile.twitter.com"
)

// profile contains information about a user.
type profile struct {
	user  string // username (without '@')
	name  string // full name
	icon  string // small (48x48) favicon URL
	image string // large (400x400) avatar URL
}

func (p *profile) displayName() string {
	return fmt.Sprintf("%s (@%s)", p.name, p.user)
}

// tweet describes a single tweet.
type tweet struct {
	id      int64
	href    string    // absolute URL to tweet
	user    string    // username (without '@')
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
	return len(t.replyUsers) > 0
}

// parse reads an HTML document containing a Twitter timeline from r and returns its tweets.
func parse(r io.Reader, ft *fetcher) (profile, []tweet, error) {
	root, err := html.Parse(r)
	if err != nil {
		return profile{}, nil, err
	}
	p := parser{fetcher: ft}
	if err := p.proc(root); err != nil {
		return profile{}, nil, err
	}
	return p.profile, p.tweets, nil
}

// section describes the section of the timeline page being parsed.
type section int

const (
	unknownSection section = iota
	profileSection
	timelineSection
)

type parser struct {
	fetcher  *fetcher
	section  section // current section being parsed
	profile  profile // information about timeline owner
	curTweet *tweet  // in-progress tweet
	tweets   []tweet // completed tweets
}

func (p *parser) proc(n *html.Node) error {
	switch p.section {
	case unknownSection:
		if isElement(n, "div") && hasClass(n, "profile") {
			p.section = profileSection
			defer func() { p.section = unknownSection }()
		} else if isElement(n, "div") && hasClass(n, "timeline") {
			p.section = timelineSection
			defer func() { p.section = unknownSection }()
		}
	case profileSection:
		switch {
		case matchFunc("td", "avatar")(n):
			if imgs := findNodes(n, func(n *html.Node) bool { return isElement(n, "img") }); len(imgs) > 0 {
				p.profile.icon = getAttr(imgs[0], "src")
				p.profile.image = strings.Replace(p.profile.icon, "_normal.", "_400x400.", 1)
			}
			return nil // skip contents
		case matchFunc("div", "fullname")(n):
			p.profile.name = cleanText(getText(n))
			return nil // skip contents
		case matchFunc("span", "screen-name")(n):
			p.profile.user = cleanText(getText(n))
			return nil // skip contents
		}
	case timelineSection:
		switch {
		case p.curTweet == nil:
			if matchFunc("table", "tweet")(n) {
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
		case matchFunc("strong", "fullname")(n):
			p.curTweet.name = cleanText(getText(n))
			return nil // skip contents
		case matchFunc("div", "username")(n):
			if hasClass(n, "tweet-reply-context") {
				// The username(s) appear inside <a> elements nested under the div.
				for _, a := range findNodes(n, func(n *html.Node) bool { return isElement(n, "a") }) {
					p.curTweet.replyUsers = append(p.curTweet.replyUsers, bareUser(cleanText(getText(a))))
				}
			} else {
				p.curTweet.user = bareUser(cleanText(getText(n)))
			}
			return nil // skip contents
		case matchFunc("td", "timestamp")(n):
			var err error
			s := strings.TrimSpace(getText(n))
			if p.curTweet.time, err = parseTime(s, time.Now()); err != nil {
				return fmt.Errorf("bad time %q: %v", s, err)
			}
			return nil // skip contents
		case matchFunc("div", "tweet-text")(n):
			var err error
			if p.curTweet.id, err = strconv.ParseInt(getAttr(n, "data-id"), 10, 64); err != nil {
				return fmt.Errorf("failed parsing ID: %v", err)
			}
			// Extract a plain-text version of the tweet first.
			p.curTweet.text = cleanText(getText(n))

			addEmbeddedContent(n, p.fetcher)
			if p.curTweet.user != p.profile.user {
				prependUserLink(n, p.curTweet.user, p.curTweet.displayName())
			}
			rewriteRelativeLinks(n)
			var b bytes.Buffer
			if err := html.Render(&b, n); err != nil {
				return fmt.Errorf("failed rendering text: %v", err)
			}
			p.curTweet.content = b.String()
			return nil // skip contents
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := p.proc(c); err != nil {
			return err
		}
	}
	return nil
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

// matchFunc is a convenient shorthand that returns a function that can be passed to findNodes.
// If tag is non-empty, only HTML elements with the given tag are matched.
// If class is non-empty, only nodes with the supplied CSS class are matched.
func matchFunc(tag, class string) func(n *html.Node) bool {
	return func(n *html.Node) bool {
		if tag != "" && !isElement(n, tag) {
			return false
		}
		if class != "" && !hasClass(n, class) {
			return false
		}
		return true
	}
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

// addEmbeddedContent adds embedded content that's linked within n.
func addEmbeddedContent(n *html.Node, ft *fetcher) {
	// Look for embedded tweets: <a data-expanded-url="https://twitter.com/someuser/status/...">.
	for _, link := range findNodes(n, func(n *html.Node) bool {
		return isElement(n, "a") && getAttr(n, "data-expanded-url") != ""
	}) {
		url := getAttr(link, "data-url")
		content, err := getTweetContent(url, ft)
		if content == nil || err != nil {
			if err != nil {
				log.Print("Couldn't fetch tweet content: ", err)
			}
			continue
		}

		debug("Adding embedded tweet ", url)

		// Insert an <hr> before the link to separate the tweet from the preceding content.
		parent := link.Parent
		parent.InsertBefore(&html.Node{Type: html.ElementNode, DataAtom: atom.Hr, Data: "hr"}, link)

		// Nest the link under a <strong> tag to set it off from the preceding content.
		strong := &html.Node{Type: html.ElementNode, DataAtom: atom.Strong, Data: "strong"}
		parent.InsertBefore(strong, link)
		parent.RemoveChild(link)
		strong.AppendChild(link)

		// Insert the content after the <strong> tag.
		parent.InsertBefore(content, strong.NextSibling)
	}

	// Look for links to image pages: <a data-pre-embedded="true" data-url="...">.
	// We do this after embedding tweets so we can insert their images too.
	for _, link := range findNodes(n, func(n *html.Node) bool {
		return isElement(n, "a") && getAttr(n, "data-pre-embedded") == "true"
	}) {
		url, err := getImageURL(getAttr(link, "data-url"), ft)
		if url == "" || err != nil {
			if err != nil {
				log.Print("Couldn't get image URL: ", err)
			}
			continue
		}
		// Replace the link's children (formerly the photo page URL) with an <img> tag.
		debug("Adding embedded image ", url)
		for link.LastChild != nil {
			link.RemoveChild(link.LastChild)
		}
		link.AppendChild(&html.Node{
			Type:     html.ElementNode,
			DataAtom: atom.Img,
			Data:     "img",
			Attr:     []html.Attribute{html.Attribute{Key: "src", Val: url}},
		})
	}
}

// getImageURL attempts to extract the underlying URL to the image from the supplied photo
// page URL, e.g. "https://twitter.com/biff_tannen/status/12813232543132445323/photo/1".
// If the URL is not a photo page, an empty string is returned.
func getImageURL(url string, ft *fetcher) (string, error) {
	// Check if we got a photo page URL. Then download and parse the image page
	// to look for a <div class="media"> with an <img> inside of it.
	if url = getMobileURL(url); !strings.Contains(url, "/photo/") {
		return "", nil
	} else if b, err := ft.fetch(url, true /* useCache */); err != nil {
		return "", fmt.Errorf("couldn't fetch %v: %v", url, err)
	} else if root, err := html.Parse(bytes.NewReader(b)); err != nil {
		return "", fmt.Errorf("couldn't parse %v: %v", url, err)
	} else if media := findNodes(root, matchFunc("div", "media")); len(media) == 0 {
		return "", fmt.Errorf("didn't find media div in %v", url)
	} else if imgs := findNodes(media[0], matchFunc("img", "")); len(imgs) == 0 {
		return "", fmt.Errorf("didn't find image in %v", url)
	} else {
		return getAttr(imgs[0], "src"), nil
	}
}

// getTweetContent attempts to extract the main content from the supplied tweet URL,
// e.g. "https://twitter.com/biff_tannen/status/12813232543132445323". If the URL is
// not a tweet page, nil is returned.
func getTweetContent(url string, ft *fetcher) (*html.Node, error) {
	if url = getMobileURL(url); !strings.Contains(url, "/status/") {
		return nil, nil
	} else if b, err := ft.fetch(url, true /* useCache */); err != nil {
		return nil, fmt.Errorf("couldn't fetch %v: %v", url, err)
	} else if root, err := html.Parse(bytes.NewReader(b)); err != nil {
		return nil, fmt.Errorf("couldn't parse %v: %v", url, err)
	} else if divs := findNodes(root, matchFunc("div", "tweet-text")); len(divs) == 0 {
		return nil, fmt.Errorf("didn't find content in %v", url)
	} else {
		div := divs[0]
		div.Parent.RemoveChild(div) // need to remove before adding to different tree
		return div, nil
	}
}

// prependUserLink prepends a link to the supplied user within n.
// This is useful for attributing retweets.
func prependUserLink(n *html.Node, user, displayName string) {
	link := &html.Node{
		Type:     html.ElementNode,
		DataAtom: atom.A,
		Data:     "a",
		Attr: []html.Attribute{
			html.Attribute{Key: "href", Val: fmt.Sprintf("%v://%v/%v", defaultScheme, defaultHost, user)},
		},
	}
	link.AppendChild(&html.Node{Type: html.TextNode, Data: displayName})

	strong := &html.Node{Type: html.ElementNode, DataAtom: atom.Strong, Data: "strong"}
	strong.AppendChild(link)

	n.InsertBefore(strong, n.FirstChild)
}

// rewriteRelativeLinks rewrites all relative links in n to be absolute.
func rewriteRelativeLinks(n *html.Node) {
	for _, link := range findNodes(n, matchFunc("a", "")) {
		for i, a := range link.Attr {
			if a.Key == "href" {
				if url, err := url.Parse(a.Val); err == nil && url.Host == "" {
					url.Scheme = defaultScheme
					url.Host = defaultHost
					debugf("Rewrote link %v to %s", a.Val, url)
					link.Attr[i].Val = url.String()
				}
			}
		}
	}
}

// getMobileURL rewrites u to be a mobile.twitter.com URL (needed for getting a basic HTML page)
// if it isn't one already. If u isn't a twitter.com URL, returns an empty string.
func getMobileURL(u string) string {
	url, err := url.Parse(u)
	if err != nil {
		return ""
	}
	if url.Host == defaultHost {
		url.Host = mobileHost
	}
	if url.Host != mobileHost {
		return ""
	}
	return url.String()
}
