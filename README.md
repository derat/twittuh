# twittuh

[![Build Status](https://travis-ci.org/derat/twittuh.svg?branch=master)](https://travis-ci.org/derat/twittuh)

This is a small Go program that scrapes a user's Twitter timeline (using the
simple HTML-only version of the site served to clients that don't support
JavaScript) and generates an RSS feed containing their tweets.

I don't have (or want, really) a Twitter account, but I'm finding myself
repeatedly clicking on a dozen or so bookmarks to check for updates. It feels
like I'm in the year 2000. Thanks to this program, I can use an RSS reader to
monitor these timelines, making me feel like I'm in the year 2005 instead.

## Usage

```
Usage: twittuh [flag]... <user> <file>
Creates an RSS feed from a Twitter user's timeline.
Flags:
  -cache-dir string
        Directory for caching downloads (default "$HOME/.cache/twittuh")
  -debug-file string
        HTML timeline file to parse for debugging
  -embeds
        Rewrite tweets to include embedded images and tweets (default true)
  -force
        Download and write feed even if there are no new tweets
  -format string
        Feed format to write ("atom", "json", "rss") (default "atom")
  -pages int
        Timeline pages to request (20 tweets/replies per page) (default 3)
  -replies
        Include the user's replies
  -skip-users string
        Comma-separated users whose tweets should be skipped
  -user-agent string
        User-Agent header to include in HTTP requests
  -verbose
        Enable verbose logging
```
