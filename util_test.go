// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import "testing"

func testCleanText(t *testing.T) {
	for _, tc := range []struct {
		orig, want string
	}{
		{"", ""},
		{" ", ""},
		{" a ", "a"},
		{" \n foo bar \t", "foo bar"},
		{"foo\nbar", "foo bar"},
		{" foo \t bar ", "foo bar"},
	} {
		if got := cleanText(tc.orig); got != tc.want {
			t.Errorf("bareUser(%q) = %q; want %q", tc.orig, got, tc.want)
		}
	}
}

func testBareUser(t *testing.T) {
	for _, tc := range []struct {
		orig, want string
	}{
		{"", ""},
		{"a", "a"},
		{"@a", "a"},
		{"@someuser", "someuser"},
		{"someuser", "someuser"},
	} {
		if got := bareUser(tc.orig); got != tc.want {
			t.Errorf("bareUser(%q) = %q; want %q", tc.orig, got, tc.want)
		}
	}
}

func testMobileURL(t *testing.T) {
	for _, tc := range []struct {
		orig, want string
	}{
		{"", ""},
		{"blah", ""},
		{"https://www.google.com/", ""},
		{"https://twitter.com/", "https://mobile.twitter.com/"},
		{"https://twitter.com/user", "https://mobile.twitter.com/user"},
		{"https://mobile.twitter.com/", "https://mobile.twitter.com/"},
		{"https://mobile.twitter.com/user", "https://mobile.twitter.com/user"},
		{"https://other.twitter.com/", ""},
		{"https://other.twitter.com/user", ""},
	} {
		if got := mobileURL(tc.orig); got != tc.want {
			t.Errorf("mobileURL(%q) = %q; want %q", tc.orig, got, tc.want)
		}
	}
}

func testAbsoluteURL(t *testing.T) {
	for _, tc := range []struct {
		orig, want string
	}{
		{"", "https://twitter.com/"},
		{"blah", "https://twitter.com/blah"},
		{"/blah", "https://twitter.com/blah"},
		{"/blah?abc=def", "https://twitter.com/blah?abc=def"},
		{"https://www.google.com/", "https://www.google.com/"},
		{"https://twitter.com/user", "https://mobile.twitter.com/user"},
	} {
		if got := absoluteURL(tc.orig); got != tc.want {
			t.Errorf("absoluteURL(%q) = %q; want %q", tc.orig, got, tc.want)
		}
	}
}
