// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	defaultScheme = "https"
	defaultHost   = "twitter.com"
	mobileHost    = "mobile.twitter.com"
)

var spaceRegexp = regexp.MustCompile(`\s+`)

// cleanText trims whitespace from the beginning and end of s and condenses repeated whitespace.
func cleanText(s string) string {
	s = strings.TrimSpace(s)
	s = spaceRegexp.ReplaceAllString(s, " ")
	return s
}

// bareUser strips off a leading '@' if present.
func bareUser(u string) string {
	if len(u) > 1 && u[0] == '@' {
		return u[1:]
	}
	return u
}

// userURL returns the canonical URL of the supplied user's timeline.
func userURL(user string) string {
	return fmt.Sprintf("%s://%s/%s", defaultScheme, defaultHost, user)
}

// mobileURL rewrites u to be a mobile.twitter.com URL (needed for getting a basic HTML page)
// if it isn't one already. If u isn't a twitter.com URL, returns an empty string.
func mobileURL(u string) string {
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

// absoluteURL rewrites s to be an absolute twitter.com URL if relative.
// If s is already absolute (regardless of host), it is returned unchanged.
func absoluteURL(s string) string {
	if s == "" {
		return s
	}
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	if u.IsAbs() {
		return s
	}
	u.Scheme = defaultScheme
	u.Host = defaultHost
	return u.String()
}
