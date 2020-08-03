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
	image string // large (400x400) avatar URL
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

// parse reads an HTML document containing a Twitter timeline from r and returns its tweets.
func parse(r io.Reader, ft *fetcher, embeds bool) (prof profile, tweets []tweet, nextURL string, err error) {
	root, err := html.Parse(r)
	if err != nil {
		return profile{}, nil, "", err
	}
	p := parser{fetcher: ft, embeds: embeds}
	if err := p.proc(root); err != nil {
		return profile{}, nil, "", err
	}
	if p.profile.user == "" {
		return p.profile, p.tweets, p.nextURL, errors.New("didn't find profile")
	}
	return p.profile, p.tweets, p.nextURL, nil
}

// section describes the section of the timeline page being parsed.
type section int

const (
	unknownSection section = iota
	profileSection
	timelineSection
)

type parser struct {
	fetcher *fetcher
	embeds  bool    // insert embedded images and tweets
	section section // current section being parsed

	profile  profile // information about timeline owner
	tweets   []tweet // completed tweets
	curTweet *tweet  // in-progress tweet
	nextURL  string  // URL from 'more' button linking to older tweets
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
		case matchFunc("td", "class=avatar")(n):
			if imgs := findNodes(n, matchFunc("img")); len(imgs) > 0 {
				p.profile.icon = getAttr(imgs[0], "src")
				p.profile.image = strings.Replace(p.profile.icon, "_normal.", "_400x400.", 1)
			}
			return nil // skip contents
		case matchFunc("div", "class=fullname")(n):
			p.profile.name = cleanText(getText(n))
			return nil // skip contents
		case matchFunc("span", "class=screen-name")(n):
			p.profile.user = cleanText(getText(n))
			return nil // skip contents
		}
	case timelineSection:
		switch {
		case p.curTweet == nil:
			if matchFunc("table", "class=tweet")(n) {
				p.curTweet = &tweet{href: absoluteURL(getAttr(n, "href"))}
				debug("Starting ", p.curTweet.href)
				defer func() {
					p.tweets = append(p.tweets, *p.curTweet)
					p.curTweet = nil
				}()
			} else if matchFunc("div", "class=w-button-more")(n) {
				if links := findNodes(n, matchFunc("a")); len(links) == 0 {
					return errors.New("no link in 'more' button")
				} else if p.nextURL = mobileURL(absoluteURL(getAttr(links[0], "href"))); p.nextURL == "" {
					return errors.New("bad link in 'more' button")
				}
			}
		// In the remaining cases, we're inside a tweet.
		case matchFunc("strong", "class=fullname")(n):
			p.curTweet.name = cleanText(getText(n))
			return nil // skip contents
		case matchFunc("div", "class=username")(n):
			if hasClass(n, "tweet-reply-context") {
				// The username(s) appear inside <a> elements nested under the div.
				for _, a := range findNodes(n, matchFunc("a")) {
					p.curTweet.replyUsers = append(p.curTweet.replyUsers, bareUser(cleanText(getText(a))))
				}
			} else {
				p.curTweet.user = bareUser(cleanText(getText(n)))
			}
			return nil // skip contents
		case matchFunc("td", "class=timestamp")(n):
			var err error
			s := strings.TrimSpace(getText(n))
			if p.curTweet.time, err = parseTime(s, time.Now()); err != nil {
				return fmt.Errorf("bad time %q: %v", s, err)
			}
			return nil // skip contents
		case matchFunc("div", "class=tweet-text")(n):
			var err error
			if p.curTweet.id, err = strconv.ParseInt(getAttr(n, "data-id"), 10, 64); err != nil {
				return fmt.Errorf("failed parsing ID: %v", err)
			}
			// Extract a plain-text version of the tweet first.
			p.curTweet.text = cleanText(getText(n))

			if p.embeds {
				// Insert images after tweets so we can insert the tweets' images too.
				addEmbeddedTweets(n, p.fetcher)
				addEmbeddedImages(n, p.fetcher)
			}
			if p.curTweet.user != p.profile.user {
				prependLink(n, p.curTweet.href, p.curTweet.displayName())
			}
			addLineBreaks(n)
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

var durationRegexp = regexp.MustCompile(`^(\d+)([smh])$`)

// parseTime parses a Twitter-supplied "timestamp".
// These can take a variety of forms:
//   - "23m" or "2h" if within the last 24 hours
//   - "Jul 9" if within the last year
//   - "25 Jun 19" if more than a year old (i.e. day-of-month first)
func parseTime(s string, now time.Time) (time.Time, error) {
	if ms := durationRegexp.FindStringSubmatch(s); ms != nil {
		quant, err := strconv.Atoi(ms[1])
		if err != nil {
			return time.Time{}, errors.New("bad quantity")
		}
		var units time.Duration
		switch ms[2] {
		case "s": // not sure if actually used
			units = time.Second
		case "m":
			units = time.Minute
		case "h":
			units = time.Hour
		default:
			return time.Time{}, errors.New("bad units") // shouldn't happen
		}
		return now.Add(-1 * time.Duration(quant) * units), nil
	}

	// Twitter doesn't supply the time or time zone for dates. Use noon GMT to make
	// it likely that we'll at least get the correct day irrespective of time zone.
	if t, err := time.Parse("Jan 2", s); err == nil {
		year := now.Year()
		if t.Month() > now.Month() || (t.Month() == now.Month() && t.Day() > now.Day()) {
			year--
		}
		t, _ = time.Parse("Jan 2 2006 15:00", s+fmt.Sprintf(" %04d 12:00", year))
		return t, nil
	}

	if t, err := time.Parse("2 Jan 06 15:00", s+" 12:00"); err == nil {
		return t, nil
	}

	return time.Time{}, errors.New("unknown format")
}

// addEmbeddedContent inserts the content of tweets that are embedded within n.
// Embeds appear as <a data-expanded-url="https://twitter.com/someuser/status/...">.
func addEmbeddedTweets(n *html.Node, ft *fetcher) {
	for _, link := range findNodes(n, matchFunc("a", "data-expanded-url")) {
		url := getAttr(link, "data-url")
		content, err := getTweetContent(url, ft)
		if content == nil || err != nil {
			if err != nil {
				debug("Couldn't add embedded tweet: ", err)

				// The tweet was probably deleted or made private.
				// Wrap the link in a strikethrough.
				parent := link.Parent
				st := &html.Node{Type: html.ElementNode, DataAtom: atom.S, Data: "s"}
				parent.InsertBefore(st, link)
				parent.RemoveChild(link)
				st.AppendChild(link)
			}
			continue
		}

		debug("Adding embedded tweet ", url)

		// Insert a <p> before the link.
		parent := link.Parent
		pg := &html.Node{Type: html.ElementNode, DataAtom: atom.P, Data: "p"}
		parent.InsertBefore(pg, link)

		// Within the <p>, add an <hr>, a <strong> containing the link, and the content <div>.
		// Sadly, <hr> elements don't seem to be displayed by the Feedly Android app.
		pg.AppendChild(&html.Node{Type: html.ElementNode, DataAtom: atom.Hr, Data: "hr"})
		pg.AppendChild(&html.Node{Type: html.ElementNode, DataAtom: atom.Strong, Data: "strong"})
		parent.RemoveChild(link) // remove before reparenting under <strong>
		pg.LastChild.AppendChild(link)
		pg.AppendChild(content)
	}
}

// addEmbeddedImages inserts <img> elemnts for images that are embedded within n.
// Links to "/photo/" URLs appear as <a data-pre-embedded="true" data-url="...">.
// Photo pages need to be fetched to get the actual image URLs.
func addEmbeddedImages(n *html.Node, ft *fetcher) {
	for _, link := range findNodes(n, matchFunc("a", "data-pre-embedded=true")) {
		url, err := getImageURL(getAttr(link, "data-url"), ft)
		if url == "" || err != nil {
			if err != nil {
				log.Print("Couldn't get image URL: ", err)
			}
			continue
		}

		debug("Adding embedded image ", url)

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
	}
}

// getImageURL attempts to extract the underlying URL to the image from the supplied photo
// page URL, e.g. "https://twitter.com/biff_tannen/status/12813232543132445323/photo/1".
// If the URL is not a photo page, an empty string is returned.
func getImageURL(url string, ft *fetcher) (string, error) {
	// Check if we got a photo page URL. Then download and parse the image page
	// to look for a <div class="media"> with an <img> inside of it.
	if url = mobileURL(url); !strings.Contains(url, "/photo/") {
		return "", nil
	} else if b, err := ft.fetch(url, true /* useCache */); err != nil {
		return "", fmt.Errorf("couldn't fetch %v: %v", url, err)
	} else if root, err := html.Parse(bytes.NewReader(b)); err != nil {
		return "", fmt.Errorf("couldn't parse %v: %v", url, err)
	} else if media := findNodes(root, matchFunc("div", "class=media")); len(media) == 0 {
		return "", fmt.Errorf("didn't find media div in %v", url)
	} else if imgs := findNodes(media[0], matchFunc("img")); len(imgs) == 0 {
		// Images deemed "sensitive material" aren't included until the user posts a form.
		// Sending another GET request for the photo page with the appropriate "show_media" and
		// "authenticity_token" parameters seems to work.
		if !strings.Contains(url, "authenticity_token") &&
			len(findNodes(media[0], matchFunc("form"))) == 1 &&
			len(findNodes(media[0], matchFunc("input", "name=show_media"))) == 1 {
			if inputs := findNodes(media[0], matchFunc("input", "name=authenticity_token")); len(inputs) == 1 {
				newURL := fmt.Sprintf("%s?show_media=1&authenticity_token=%v", url, getAttr(inputs[0], "value"))
				return getImageURL(newURL, ft)
			}
		}
		return "", fmt.Errorf("didn't find image in %v", url)
	} else {
		return getAttr(imgs[0], "src"), nil
	}
}

// getTweetContent attempts to extract the main content from the supplied tweet URL,
// e.g. "https://twitter.com/biff_tannen/status/12813232543132445323". If the URL is
// not a tweet page, nil is returned.
func getTweetContent(url string, ft *fetcher) (*html.Node, error) {
	if url = mobileURL(url); !strings.Contains(url, "/status/") {
		return nil, nil
	} else if b, err := ft.fetch(url, true /* useCache */); err != nil {
		return nil, fmt.Errorf("couldn't fetch %v: %v", url, err)
	} else if root, err := html.Parse(bytes.NewReader(b)); err != nil {
		return nil, fmt.Errorf("couldn't parse %v: %v", url, err)
	} else if divs := findNodes(root, matchFunc("div", "class=tweet-text")); len(divs) == 0 {
		return nil, fmt.Errorf("didn't find content in %v", url)
	} else {
		div := divs[0]
		div.Parent.RemoveChild(div) // need to remove before adding to different tree
		return div, nil
	}
}

// addLineBreaks replaces newlines in text nodes under n with <br> elements.
// Feedly strips out all CSS, so we can't use "white-space: pre-wrap" like Twitter's web UI.
func addLineBreaks(n *html.Node) {
	for _, text := range findNodes(n, func(n *html.Node) bool { return n.Type == html.TextNode }) {
		// Twitter looks like it also inserts innocuous spaces and newlines between <div> elements.
		// Leave these alone.
		if strings.TrimSpace(text.Data) == "" {
			continue
		}
		parts := strings.Split(text.Data, "\n")
		if len(parts) < 2 {
			continue
		}

		prev := text
		parent := text.Parent
		for i, p := range parts {
			// Don't bother adding empty text nodes.
			if p != "" {
				t := &html.Node{Type: html.TextNode, Data: p}
				parent.InsertBefore(t, prev.NextSibling)
				prev = t
			}
			// After each text node but the last one, add a line break.
			if i < len(parts)-1 {
				br := &html.Node{Type: html.ElementNode, DataAtom: atom.Br, Data: "br"}
				parent.InsertBefore(br, prev.NextSibling)
				prev = br
			}
		}

		// Remove the original text node now that it's been replaced.
		parent.RemoveChild(text)
	}
}

// prependLink prepends a bolded link to url within n.
func prependLink(n *html.Node, url, displayName string) {
	link := &html.Node{
		Type:     html.ElementNode,
		DataAtom: atom.A,
		Data:     "a",
		Attr:     []html.Attribute{html.Attribute{Key: "href", Val: url}},
	}
	link.AppendChild(&html.Node{Type: html.TextNode, Data: displayName})

	strong := &html.Node{Type: html.ElementNode, DataAtom: atom.Strong, Data: "strong"}
	strong.AppendChild(link)
	n.InsertBefore(strong, n.FirstChild)
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
