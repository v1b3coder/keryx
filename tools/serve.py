#!/usr/bin/env python3
"""Serve a directory over HTTP with permissive CORS.

The PWA runs on its own origin (Vite dev server or GitHub Pages), so the local
demo artifact must be served with CORS headers for the browser to fetch it.
"""

from __future__ import annotations

import argparse
import functools
import http.server


class CorsHandler(http.server.SimpleHTTPRequestHandler):
    def end_headers(self) -> None:
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Cache-Control", "no-store")
        super().end_headers()

    def log_message(self, fmt: str, *args: object) -> None:
        # keep the console readable; uncomment for request logging
        pass


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", nargs="?", default="demo", help="directory to serve")
    parser.add_argument("--port", type=int, default=8000)
    parser.add_argument("--bind", default="0.0.0.0")
    args = parser.parse_args()

    handler = functools.partial(CorsHandler, directory=args.directory)
    with http.server.ThreadingHTTPServer((args.bind, args.port), handler) as httpd:
        print(f"Serving {args.directory} at http://localhost:{args.port} (CORS enabled)")
        try:
            httpd.serve_forever()
        except KeyboardInterrupt:
            print()


if __name__ == "__main__":
    main()
