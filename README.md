# twittuh

[![Build Status](https://travis-ci.org/derat/twittuh.svg?branch=master)](https://travis-ci.org/derat/twittuh)

`twittuh` is a Go program that loads a user's Twitter timeline using a [headless
Chrome] browser and generates an RSS feed containing their tweets.

I don't have (or want) a Twitter account, and I found myself repeatedly clicking
on a dozen or so bookmarks to check for updates. It felt like I was in the
year 2000. Thanks to this program, I can use an RSS reader to monitor these
timelines, making me feel like I'm in the year 2005 instead.

This program originally scraped the plain-HTML "Legacy Twitter" pages that were
served to old browsers, but [Twitter shut down the interface] on 2020-12-16. Now
this program uses Chrome to construct the complete DOM (i.e. by executing
JavaScript) and parses that instead.

[headless Chrome]: https://developers.google.com/web/updates/2017/04/headless-chrome
[Twitter shut down the interface]: https://screenrant.com/twitter-legacy-nintendo-3ds-shut-down-date-december-2020/

## Installation

To compile and install `twittuh`, run the following (after [installing Go] if
you don't have it already):

```
$ go install
```

The `twittuh` executable will be installed to `$GOPATH/bin` (or `$GOBIN` if
you've set it directly).

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

[installing Go]: https://golang.org/doc/install
[chromedp package]: https://github.com/chromedp/chromedp

## Usage

```
Usage: twittuh [flag]... <user> <file>
Creates an RSS feed from a Twitter user's timeline.
Pass '-' for <file> to write feed to stdout.
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
        Seconds to wait for page render (default 2)
  -proxy string
        Optional proxy server (e.g. "socks5://localhost:9050")
  -replies
        Include the user's replies
  -serve string
        Listen for requests over HTTP (e.g. "0.0.0.0:8080")
  -show-sensitive
        Show sensitive content in tweets (default true)
  -show-sensitive-delay int
        Seconds to wait after showing sensitive content (default 2)
  -simplify
        Simplify HTML in feed (default true)
  -skip-users string
        Comma-separated users whose tweets should be skipped
  -tor-control string
        Interface for resetting Tor circuits after fetch fails (e.g. "0.0.0.0:9051")
  -tweet-timeout int
        Timeout for loading tweets in seconds
  -verbose
        Enable verbose logging
```

## Tips

### Tor

Twitter seems to haphazardly block unauthenticated timeline requests. When this
happens, the timeline page itself (e.g. `https://twitter.com/NWS`) loads but the
XHR to load the actual tweets fails. The page shows a `Something went wrong.`
message and a `Try again` button.

I suspect that some cloud providers' networks are proactively blocked.
Fortunately, it's easy to route requests through [Tor].

On a Debian system, run the following as the `root` user to install Tor:

```
# apt install tor
```

You can then pass `-proxy socks5://localhost:9050` to `twittuh` to tell it to
instruct Chrome to use the Tor proxy.

Some Tor exit nodes also appear to be blocked. You can tell Tor to reset its
circuits (likely resulting in a new exit IP) by sending a `NEWNYM` command to
its control socket (see `resetTorCircuits` in [main.go](./main.go)) or
(allegedly) by sending a `HUP` signal to the `tor` process to tell it to reload
its configuration.

[Tor]: https://www.torproject.org/

### Example script

The [scrape_twitter.py.example] file in this repository may be helpful if you
want to run `twittuh` periodically via cron to monitor multiple timelines. The
timeouts have been tweaked for a slow VPS that's using Tor. You'll want to edit
the variables near the top of the file for your system and rename it to
`scrape_twitter.py`.

Pay particular attention to the `INTERVAL_SEC` variable, which specifies the
total amount of time allocated to each invocation of the script. If you want to
check each timeline once every four hours, change `INTERVAL_SEC` to `4 * 3600`
and add a line like the following to your crontab:

```cron
30 */4 * * * /path/to/scrape_twitter.py
```

[scrape_twitter.py.example]: ./scrape_twitter.py.example

### Docker

[Docker] can be used to run `twittuh -serve` in a container. The
[Dockerfile](./Dockerfile) in this repository builds a container image that runs
an instance of `twittuh` listening for HTTP `GET` requests on port 8080. Tor is
also installed. The HTTP endpoint accepts `user` and `format` query parametrs.

When executed in this directory, the following command uses [Cloud Build] to
build a container and submit it to the [Container Registry].

```
$ gcloud --project ${PROJECT_ID} builds submit \
    --tag gcr.io/${PROJECT_ID}/twittuh
```

After updating the container image, you can run a command like the following to
make a [Compute Engine] instance reload it:

```
$ gcloud --project ${PROJECT_ID} compute instances update-container \
    ${GCE_INSTANCE} --container-image gcr.io/${PROJECT_ID}/twittuh
```

[Docker]: https://www.docker.com/
[Cloud Build]: https://cloud.google.com/build
[Container Registry]: https://cloud.google.com/container-registry
[Compute Engine]: https://cloud.google.com/compute
