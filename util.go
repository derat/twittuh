// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

// bareUser strips off a leading '@' if present.
func bareUser(u string) string {
	if len(u) > 1 && u[0] == '@' {
		return u[1:]
	}
	return u
}

// userURL returns the canonical URL of the supplied user's timeline.
func userURL(user string) string {
	return "https://twitter.com/" + user
}
