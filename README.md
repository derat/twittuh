# twittuh

[![Build Status](https://travis-ci.org/derat/twittuh.svg?branch=master)](https://travis-ci.org/derat/twittuh)

This is a small Go program that scrapes a user's Twitter timeline using a
[headless Chrome] browser and generates an RSS feed containing their tweets.

I don't have (or want, really) a Twitter account, but I'm finding myself
repeatedly clicking on a dozen or so bookmarks to check for updates. It feels
like I'm in the year 2000. Thanks to this program, I can use an RSS reader to
monitor these timelines, making me feel like I'm in the year 2005 instead.

This program originally scraped the plain-HTML "Legacy Twitter" pages that were
served to old browsers, but Twitter apparently shut down the interface on
2020-12-16. Now this program uses Chrome to construct the complete DOM (i.e. by
executing JavaScript) and parses that instead.

[headless Chrome]: https://developers.google.com/web/updates/2017/04/headless-chrome

## Setup

Headless Chrome is controlled by the [chromedp package], but you must install
Chrome manually. I was able to do this on a Debian `buster` amd64 server by
running the following as the `root` user:

```
# wget https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb
# dpkg -i google-chrome-stable_current_amd64.deb
# apt install -f
```

(The `dpkg` command will likely fail due to missing dependencies, which can be
installed using the subsequent `apt` command.)

[chromedp package]: https://github.com/chromedp/chromedp

## Usage

```
Usage: twittuh [flag]... <user> <file>
Creates an RSS feed from a Twitter user's timeline.
Flags:
  -browser-size string
        Browser viewport size (default "1024x8192")
  -cache-dir string
        Chrome cache directory
  -debug-chrome
        Log noisy Chrome debug messages
  -debug-file string
        HTML timeline file to parse for debugging
  -dump-dom
        Dump the timeline DOM to stdout for debugging
  -fetch-retries int
        Number of times to retry fetching
  -fetch-timeout int
        Fetch timeout in seconds
  -force
        Write feed even if there are no new tweets
  -format string
        Feed format to write ("atom", "json", "rss") (default "atom")
  -page-settle-delay int
        Time to wait for page render in seconds (default 2)
  -proxy string
        Optional proxy server (e.g. "socks5://localhost:9050")
  -replies
        Include the user's replies
  -simplify
        Simplify HTML in feed (default true)
  -skip-users string
        Comma-separated users whose tweets should be skipped
  -tweet-timeout int
        Timeout for loading tweets in seconds
  -verbose
        Enable verbose logging
```
