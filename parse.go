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
)

const (
	defaultScheme = "https"
	defaultHost   = "twitter.com"
)

// tweet describes a single tweet.
type tweet struct {
	id      int64
	href    string    // absolute URL to tweet
	name    string    // display name
	user    string    // username (with '@')
	time    time.Time // approximate (Twitter just gives us age)
	content string

	replyUsers []string // empty if not reply
}

// parse reads an HTML document containing a Twitter timeline from r and returns its tweets.
func parse(r io.Reader) ([]tweet, error) {
	root, err := html.Parse(r)
	if err != nil {
		return nil, err
	}
	p := parser{}
	if err := p.proc(root); err != nil {
		return nil, err
	}
	return p.tweets, nil
}

type parser struct {
	timeline bool    // true while in timeline div
	curTweet *tweet  // in-progress tweet
	tweets   []tweet // completed tweets
}

func (p *parser) proc(n *html.Node) error {
	switch {
	case !p.timeline:
		if elementClass(n, "div", "timeline") {
			p.timeline = true
			defer func() { p.timeline = false }()
		}
	case p.curTweet == nil:
		if elementClass(n, "table", "tweet") {
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
	default:
		switch {
		case elementClass(n, "strong", "fullname"):
			p.curTweet.name = cleanText(getText(n))
		case elementClass(n, "div", "username"):
			if hasClass(n, "tweet-reply-context") {
				// The username(s) appear inside <a> elements nested under the div.
				for _, a := range findNodes(n, func(n *html.Node) bool { return n.Type == html.ElementNode && n.Data == "a" }) {
					p.curTweet.replyUsers = append(p.curTweet.replyUsers, strings.TrimSpace(getText(a)))
				}
			} else {
				p.curTweet.user = strings.TrimSpace(getText(n))
			}
		case elementClass(n, "td", "timestamp"):
			s := strings.TrimSpace(getText(n))
			d, err := parseDuration(s)
			if err != nil {
				return fmt.Errorf("bad time %q: %v", s, err)
			}
			p.curTweet.time = time.Now().Add(-d)
		case elementClass(n, "div", "tweet-text"):
			var err error
			if p.curTweet.id, err = strconv.ParseInt(getAttr(n, "data-id"), 10, 64); err != nil {
				return fmt.Errorf("failed parsing ID: %v", err)
			}
			var b bytes.Buffer
			if err := html.Render(&b, n); err != nil {
				return fmt.Errorf("failed rendering text: %v", err)
			}
			p.curTweet.content = b.String()
			// TODO: Also produce a plain-text rendering?
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

// elementClass returns true if n is an HTML element with the supplied tag type and CSS class.
func elementClass(n *html.Node, tag, class string) bool {
	return n.Type == html.ElementNode && n.Data == tag && hasClass(n, class)
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

// rewriteURL rewrites s to be an absolute URL.
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

var durationRegexp = regexp.MustCompile(`^(\d+)([smhd])$`)

// parseDuration parses a Twitter-supplied "timestamp", e.g. "23m" or "2h".
func parseDuration(s string) (time.Duration, error) {
	matches := durationRegexp.FindStringSubmatch(s)
	if matches == nil {
		return 0, errors.New("invalid duration")
	}

	v, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", matches[1])
	}

	var units time.Duration
	switch matches[2] {
	case "s":
		units = time.Second
	case "m":
		units = time.Minute
	case "h":
		units = time.Hour
	case "d":
		units = 24 * time.Hour // busted for DST, but what can you do
	default:
		return 0, fmt.Errorf("invalid units %q", matches[2]) // shouldn't happen
	}

	return time.Duration(v) * units, nil
}
